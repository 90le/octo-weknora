-- Keep the ledger across rollback: deleting delivery evidence can cause
-- duplicates. Stop outbound/recovery before running older application code.
SELECT 1;
