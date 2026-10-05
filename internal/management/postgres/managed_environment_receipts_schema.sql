UPDATE management.purges SET receipts=jsonb_set(receipts,'{execution}',
    ((receipts->'execution')-'hosted_pending') || jsonb_build_object('environments_pending',receipts->'execution'->'hosted_pending'))
WHERE receipts->'execution' ? 'hosted_pending';
