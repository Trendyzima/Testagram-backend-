-- Marketplace listing filters and explicit display currency.
BEGIN;
ALTER TABLE testagram_local.marketplace_products
 ADD COLUMN IF NOT EXISTS views_count BIGINT NOT NULL DEFAULT 0 CHECK(views_count>=0),
 ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'other',
 ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT 'USD';
ALTER TABLE testagram_local.marketplace_products
 ADD CONSTRAINT testagram_marketplace_category_check CHECK(category IN ('digital','handmade','fashion','art','food','electronics','other'));
ALTER TABLE testagram_local.marketplace_products
 ADD CONSTRAINT testagram_marketplace_currency_check CHECK(currency ~ '^[A-Z]{3}$');
CREATE INDEX IF NOT EXISTS testagram_marketplace_category_idx ON testagram_local.marketplace_products(category,is_featured DESC,sales_count DESC) WHERE is_active;
COMMIT;
