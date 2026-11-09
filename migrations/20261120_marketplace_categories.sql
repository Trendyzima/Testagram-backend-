-- Marketplace listing filters and explicit display currency.
-- Safe for installations created before categories were constrained and for
-- deployment tooling that replays already-applied SQL files.
BEGIN;

ALTER TABLE testagram_local.marketplace_products
 ADD COLUMN IF NOT EXISTS views_count BIGINT NOT NULL DEFAULT 0 CHECK(views_count>=0),
 ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'other',
 ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT 'USD';

-- The preceding marketplace migration allowed any short category string.
-- Normalize legacy values before adding the finite category constraint so
-- existing listings cannot make this migration fail during deployment.
UPDATE testagram_local.marketplace_products
SET category = CASE
  WHEN lower(btrim(category)) IN ('digital','handmade','fashion','art','food','electronics','other')
    THEN lower(btrim(category))
  WHEN lower(btrim(category)) IN ('clothing','apparel','wearables') THEN 'fashion'
  WHEN lower(btrim(category)) IN ('craft','crafts','hand-crafted','handcrafted') THEN 'handmade'
  WHEN lower(btrim(category)) IN ('software','download','downloads','ebook','ebooks') THEN 'digital'
  WHEN lower(btrim(category)) IN ('painting','paintings','illustration','illustrations') THEN 'art'
  WHEN lower(btrim(category)) IN ('grocery','groceries','restaurant','restaurants') THEN 'food'
  WHEN lower(btrim(category)) IN ('gadgets','computers','computer','phones','mobile') THEN 'electronics'
  ELSE 'other'
END
WHERE category IS NULL
   OR lower(btrim(category)) NOT IN ('digital','handmade','fashion','art','food','electronics','other');

-- Keep the migration safe when deployment tooling replays the file.
DO $constraints$
BEGIN
 IF NOT EXISTS (
   SELECT 1 FROM pg_constraint
   WHERE conrelid='testagram_local.marketplace_products'::regclass
     AND conname='testagram_marketplace_category_check'
 ) THEN
   ALTER TABLE testagram_local.marketplace_products
     ADD CONSTRAINT testagram_marketplace_category_check
     CHECK(category IN ('digital','handmade','fashion','art','food','electronics','other'));
 END IF;

 IF NOT EXISTS (
   SELECT 1 FROM pg_constraint
   WHERE conrelid='testagram_local.marketplace_products'::regclass
     AND conname='testagram_marketplace_currency_check'
 ) THEN
   ALTER TABLE testagram_local.marketplace_products
     ADD CONSTRAINT testagram_marketplace_currency_check
     CHECK(currency ~ '^[A-Z]{3}$');
 END IF;
END;
$constraints$;

CREATE INDEX IF NOT EXISTS testagram_marketplace_category_idx
 ON testagram_local.marketplace_products(category,is_featured DESC,sales_count DESC)
 WHERE is_active;

COMMIT;
