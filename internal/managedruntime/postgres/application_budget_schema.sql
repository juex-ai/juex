-- Backfill frozen job metadata for retry identity, including settled receipts.
-- No execution state, attempt request, cancellation or counter is changed.
UPDATE runtime.application_jobs
SET request = jsonb_set(request, '{model_budget}', '{"context_window":16384,"max_output":4096}'::jsonb)
WHERE application = 'memory' AND request IS NOT NULL AND NOT request ? 'model_budget';
