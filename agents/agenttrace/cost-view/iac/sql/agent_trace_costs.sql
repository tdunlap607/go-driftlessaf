-- Cost estimates use Vertex AI Global region pricing for both Anthropic Claude
-- and Gemini, since driftlessaf calls Claude via Vertex
-- (public/go-driftlessaf/agents/metaagent/claude.go).
--
-- Costs are summed over the per-call records in turns[]. Each turn carries
-- its own input / output / cache_read / cache_creation token counts, so
-- the cache discount and surcharge apply per call rather than once at the
-- trace level. Trace-level token fields were retired in DEV-1140 — they
-- were last-turn snapshots from an assigning RecordTokenUsage and
-- undercounted real spend on multi-turn traces by ~28x.
--
-- Tier matching is per-call (each turn classified independently against 200K)
-- which matches Anthropic and Vertex billing semantics. All four cost
-- columns (input / output / cache_read / cache_creation) honor the Large
-- Context tier identically — same gating predicate, same per-turn
-- granularity. Sonnet 4.6 on Vertex Global is uniform-priced so the tier
-- never engages today; the logic is in place for Sonnet 4.5 / Gemini 2.5
-- Pro / 3.x Pro traffic.
--
-- NULL semantics: when turns[] is empty (non-LLM traces from mcptool /
-- mcp-auth-test, or any future caller that doesn't open a turn), all
-- *_cost_usd columns are NULL — the SUM over an empty UNNEST is NULL,
-- not 0, and the trace-level fallback that COALESCE'd to 0 was retired
-- in DEV-1140. Downstream consumers must IFNULL these columns when
-- aggregating, or filter to ARRAY_LENGTH(turns) > 0 first. We could
-- IFNULL them here instead, but NULL is the more honest signal: it
-- distinguishes "no LLM call" from "LLM call costing $0" and forces
-- consumers to make an explicit choice.
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
  -- Per-call sums over turns[]. Each turn picks its own tier based on
  -- that call's input_tokens against the 200K threshold.
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
  ) AS input_cost_usd,
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
  ) AS output_cost_usd,
  (
    SELECT SUM(
      COALESCE(turn.cache_read_tokens, 0) *
        IF(turn.input_tokens > 200000 AND m.pricing_model IN (
             'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
           ),
           p_large.cache_read_price,
           p_std.cache_read_price)
    )
    FROM UNNEST(m.turns) turn
  ) AS cache_read_cost_usd,
  (
    SELECT SUM(
      COALESCE(turn.cache_creation_tokens, 0) *
        IF(turn.input_tokens > 200000 AND m.pricing_model IN (
             'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
           ),
           p_large.cache_creation_price,
           p_std.cache_creation_price)
    )
    FROM UNNEST(m.turns) turn
  ) AS cache_creation_cost_usd,
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
      + COALESCE(turn.cache_read_tokens, 0) *
        IF(turn.input_tokens > 200000 AND m.pricing_model IN (
             'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
           ),
           p_large.cache_read_price,
           p_std.cache_read_price)
      + COALESCE(turn.cache_creation_tokens, 0) *
        IF(turn.input_tokens > 200000 AND m.pricing_model IN (
             'claude-sonnet-4-5','gemini-2.5-pro','gemini-3-pro-preview','gemini-3.1-pro-preview'
           ),
           p_large.cache_creation_price,
           p_std.cache_creation_price)
    )
    FROM UNNEST(m.turns) turn
  ) AS total_cost_usd
FROM matched m
LEFT JOIN prices p_std
  ON p_std.pricing_model = m.pricing_model
 AND p_std.pricing_tier  = 'Standard'
LEFT JOIN prices p_large
  ON p_large.pricing_model = m.pricing_model
 AND p_large.pricing_tier  = 'Large Context'
