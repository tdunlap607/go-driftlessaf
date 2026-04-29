-- Cost estimates use Vertex AI Global region pricing for both Anthropic Claude
-- and Gemini, since driftlessaf calls Claude via Vertex
-- (public/go-driftlessaf/agents/metaagent/claude.go).
--
-- Cost is summed over the per-call records in turns[] when present, since the
-- trace-level token columns are last-turn snapshots (RecordTokenUsage assigns
-- rather than accumulates) — using them undercounted real spend on 2026-04-27
-- by ~28x. We fall back to trace-level fields only when turns[] is empty so
-- single-call traces still get costed.
--
-- Per-turn cache token counts are not recorded in turns[]; cache discount /
-- surcharge is therefore only applied on the trace-level fallback path. This
-- contributes a sub-percent error on the sum-of-turns path.
--
-- Tier matching is per-call (each turn classified independently against 200K)
-- which matches Anthropic and Vertex billing semantics. Sonnet 4.6 on Vertex
-- Global is uniform-priced so the tier never engages today; the logic is in
-- place for Sonnet 4.5 / Gemini 2.5 Pro / 3.x Pro traffic.
--
-- Source: https://cloud.google.com/gemini-enterprise-agent-platform/generative-ai/pricing
WITH prices AS (
  SELECT * FROM UNNEST([
    -- USD per token = page price / 1e6
    -- Claude (Vertex Global). Opus 4.5/4.6/4.7, Sonnet 4.6, Haiku 4.5: uniform across context size.
    STRUCT(
      'claude-opus-4-7' AS pricing_model, 'Standard' AS pricing_tier,
      5.0e-6 AS input_price, 2.5e-5 AS output_price,
      5.0e-7 AS cache_read_price, 6.25e-6 AS cache_creation_price),
    STRUCT('claude-opus-4-6',     'Standard',      5.0e-6, 2.5e-5,  5.0e-7, 6.25e-6),
    STRUCT('claude-opus-4-5',     'Standard',      5.0e-6, 2.5e-5,  5.0e-7, 6.25e-6),
    STRUCT('claude-sonnet-4-6',   'Standard',      3.0e-6, 1.5e-5,  3.0e-7, 3.75e-6),
    -- Sonnet 4.5 has a >200K Large Context tier on Vertex Global; Sonnet 4.6 does not.
    STRUCT('claude-sonnet-4-5',   'Standard',      3.0e-6, 1.5e-5,  3.0e-7, 3.75e-6),
    STRUCT('claude-sonnet-4-5',   'Large Context', 6.0e-6, 2.25e-5, 6.0e-7, 7.5e-6),
    STRUCT('claude-haiku-4-5',    'Standard',      1.0e-6, 5.0e-6,  1.0e-7, 1.25e-6),
    -- Gemini (Vertex). Cache writes are not separately billed.
    STRUCT('gemini-2.5-pro',                'Standard',      1.25e-6, 1.0e-5, 1.25e-7, 0.0),
    STRUCT('gemini-2.5-pro',                'Large Context', 2.5e-6,  1.5e-5, 2.5e-7,  0.0),
    STRUCT('gemini-2.5-flash',              'Standard',      3.0e-7,  2.5e-6, 3.0e-8,  0.0),
    STRUCT('gemini-2.5-flash-lite',         'Standard',      1.0e-7,  4.0e-7, 2.5e-8,  0.0),
    STRUCT('gemini-2.0-flash',              'Standard',      1.0e-7,  4.0e-7, 0.0,     0.0),
    STRUCT('gemini-2.0-flash-lite',         'Standard',      7.5e-8,  3.0e-7, 0.0,     0.0),
    STRUCT('gemini-3-pro-preview',          'Standard',      2.0e-6,  1.2e-5, 2.0e-7,  0.0),
    STRUCT('gemini-3-pro-preview',          'Large Context', 4.0e-6,  1.8e-5, 4.0e-7,  0.0),
    STRUCT('gemini-3.1-pro-preview',        'Standard',      2.0e-6,  1.2e-5, 2.0e-7,  0.0),
    STRUCT('gemini-3.1-pro-preview',        'Large Context', 4.0e-6,  1.8e-5, 4.0e-7,  0.0),
    STRUCT('gemini-3-flash-preview',        'Standard',      5.0e-7,  3.0e-6, 5.0e-8,  0.0),
    STRUCT('gemini-3.1-flash-lite-preview', 'Standard',      2.5e-7,  1.5e-6, 2.5e-8,  0.0)
  ])
),
matched AS (
  SELECT
    t.*,
    CASE
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(anthropic/)?claude-opus-4-7$')                         THEN 'claude-opus-4-7'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(anthropic/)?claude-opus-4-6$')                         THEN 'claude-opus-4-6'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(anthropic/)?claude-opus-4-5(-20251101)?$')             THEN 'claude-opus-4-5'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(anthropic/)?claude-sonnet-4-6$')                       THEN 'claude-sonnet-4-6'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(anthropic/)?claude-sonnet-4-5(-20250929)?$')           THEN 'claude-sonnet-4-5'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(anthropic/)?claude-haiku-4-5(-20251001)?$')            THEN 'claude-haiku-4-5'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-2\.5-pro$')                            THEN 'gemini-2.5-pro'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-2\.5-flash$')                          THEN 'gemini-2.5-flash'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-2\.5-flash-lite$')                     THEN 'gemini-2.5-flash-lite'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-2\.0-flash(-001)?$')                   THEN 'gemini-2.0-flash'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-2\.0-flash-lite(-preview)?(-02-05)?$') THEN 'gemini-2.0-flash-lite'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-3-pro-preview$')                       THEN 'gemini-3-pro-preview'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-3\.1-pro-preview(-customtools)?$')     THEN 'gemini-3.1-pro-preview'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-3-flash-preview$')                     THEN 'gemini-3-flash-preview'
      WHEN REGEXP_CONTAINS(LOWER(IFNULL(t.model, '')), r'^(google/)?gemini-3\.1-flash-lite-preview$')             THEN 'gemini-3.1-flash-lite-preview'
      ELSE NULL
    END AS pricing_model
  FROM `${project_id}.${dataset_id}.${source_table_id}` t
)
SELECT
  m.*,
  -- Per-call sum when turns[] has entries; trace-level fallback otherwise.
  -- The fallback path is the only one where cache costs apply, since per-turn
  -- cache counts aren't recorded in turns[].
  COALESCE(
    (
      SELECT SUM(
        COALESCE(turn.input_tokens, 0) *
          IF(turn.input_tokens > 200000 AND m.pricing_model IN (
               'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
             ),
             p_large.input_price,
             p_std.input_price)
      )
      FROM UNNEST(m.turns) turn
    ),
    COALESCE(m.input_tokens, 0) * p_std.input_price
  ) AS input_cost_usd,
  COALESCE(
    (
      SELECT SUM(
        COALESCE(turn.output_tokens, 0) *
          IF(turn.input_tokens > 200000 AND m.pricing_model IN (
               'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
             ),
             p_large.output_price,
             p_std.output_price)
      )
      FROM UNNEST(m.turns) turn
    ),
    COALESCE(m.output_tokens, 0) * p_std.output_price
  ) AS output_cost_usd,
  IF(ARRAY_LENGTH(m.turns) = 0,
     COALESCE(m.cache_read_tokens, 0) * p_std.cache_read_price,
     0.0) AS cache_read_cost_usd,
  IF(ARRAY_LENGTH(m.turns) = 0,
     COALESCE(m.cache_creation_tokens, 0) * p_std.cache_creation_price,
     0.0) AS cache_creation_cost_usd,
  -- Source of the cost figure: 'turns' (per-call sum) or 'trace_fallback'
  -- (trace-level snapshot used because turns[] is empty). Lets dashboards
  -- distinguish complete from approximate rows.
  IF(ARRAY_LENGTH(m.turns) > 0, 'turns', 'trace_fallback') AS cost_source,
  COALESCE(
    (
      SELECT SUM(
        COALESCE(turn.input_tokens, 0) *
          IF(turn.input_tokens > 200000 AND m.pricing_model IN (
               'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
             ),
             p_large.input_price,
             p_std.input_price)
        + COALESCE(turn.output_tokens, 0) *
          IF(turn.input_tokens > 200000 AND m.pricing_model IN (
               'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
             ),
             p_large.output_price,
             p_std.output_price)
      )
      FROM UNNEST(m.turns) turn
    ),
    COALESCE(m.input_tokens, 0)          * p_std.input_price
      + COALESCE(m.output_tokens, 0)         * p_std.output_price
      + COALESCE(m.cache_read_tokens, 0)     * p_std.cache_read_price
      + COALESCE(m.cache_creation_tokens, 0) * p_std.cache_creation_price
  ) AS total_cost_usd
FROM matched m
LEFT JOIN prices p_std
  ON p_std.pricing_model = m.pricing_model
 AND p_std.pricing_tier  = 'Standard'
LEFT JOIN prices p_large
  ON p_large.pricing_model = m.pricing_model
 AND p_large.pricing_tier  = 'Large Context'
