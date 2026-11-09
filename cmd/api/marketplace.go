package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

var marketplaceCategories = map[string]struct{}{
	"digital": {}, "handmade": {}, "fashion": {}, "art": {},
	"food": {}, "electronics": {}, "other": {},
}

func (s *service) marketplaceProductsList(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MARKETPLACE_UNAVAILABLE", "Marketplace is unavailable", id))
		return
	}

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MARKETPLACE_LIMIT", "Limit must be between 1 and 100", id))
			return
		}
		limit = n
	}
	category := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category")))
	if category == "all" {
		category = ""
	}
	if category != "" {
		if _, ok := marketplaceCategories[category]; !ok {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MARKETPLACE_CATEGORY", "Category is not supported", id))
			return
		}
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 160 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MARKETPLACE_QUERY", "Product search is too long", id))
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT p.id::text, p.owner_id::text, p.name, COALESCE(p.description,''), p.category,
		       p.price::text, p.currency, COALESCE(p.image_url,''), COALESCE(p.external_link,''),
		       p.avg_rating, p.review_count, p.views_count, p.sales_count,
		       p.is_featured, p.created_at, a.username, COALESCE(a.display_name,''),
		       COALESCE(a.avatar_key,''), COALESCE(a.verified_tier,'')
		FROM testagram_local.marketplace_products p
		JOIN testagram_local.accounts a ON a.user_id = p.owner_id
		WHERE p.is_active
		  AND ($1 = '' OR p.category = $1)
		  AND ($2 = '' OR p.name ILIKE '%' || $2 || '%' OR p.description ILIKE '%' || $2 || '%')
		ORDER BY p.is_featured DESC, p.views_count DESC, p.sales_count DESC, p.created_at DESC, p.id DESC
		LIMIT $3
	`, category, query, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MARKETPLACE_PRODUCTS_FAILED", "Marketplace products could not be loaded", id))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var productID, ownerID, name, description, itemCategory, price, currency string
		var imageURL, externalLink, username, displayName, avatarKey, verifiedTier string
		var rating float64
		var reviews, views, sales int64
		var featured bool
		var created time.Time
		if err := rows.Scan(
			&productID, &ownerID, &name, &description, &itemCategory,
			&price, &currency, &imageURL, &externalLink,
			&rating, &reviews, &views, &sales, &featured, &created,
			&username, &displayName, &avatarKey, &verifiedTier,
		); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MARKETPLACE_PRODUCTS_FAILED", "Marketplace product records could not be read", id))
			return
		}
		profile := map[string]any{
			"id": ownerID, "username": username, "display_name": displayName,
			"avatar_url": avatarKey, "verified_tier": verifiedTier,
		}
		items = append(items, map[string]any{
			"id": productID, "user_id": ownerID, "owner_id": ownerID,
			"name": name, "description": description, "category": itemCategory,
			"price": price, "currency": currency, "image_url": imageURL,
			"external_link": externalLink, "avg_rating": rating,
			"review_count": reviews, "views_count": views, "sales_count": sales,
			"is_featured": featured, "created_at": created,
			"user_profiles": profile,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MARKETPLACE_PRODUCTS_FAILED", "Marketplace product records could not be read", id))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"items": items,
			"next_cursor": nil,
		},
	})
}

func (s *service) marketplaceProductGet(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	productID := strings.ToLower(strings.TrimSpace(r.PathValue("product_id")))
	if !socialUUIDPattern.MatchString(productID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MARKETPLACE_PRODUCT_ID", "Product ID is invalid", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MARKETPLACE_UNAVAILABLE", "Marketplace is unavailable", id))
		return
	}

	var ownerID, name, description, category, price, currency string
	var imageURL, externalLink, username, displayName, avatarKey, verifiedTier string
	var rating float64
	var reviews, views, sales int64
	var featured bool
	var created time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT p.owner_id::text,p.name,COALESCE(p.description,''),p.category,p.price::text,p.currency,
		       COALESCE(p.image_url,''),COALESCE(p.external_link,''),p.avg_rating,p.review_count,p.views_count,
		       p.sales_count,p.is_featured,p.created_at,
		       a.username,COALESCE(a.display_name,''),COALESCE(a.avatar_key,''),COALESCE(a.verified_tier,'')
		FROM testagram_local.marketplace_products p
		JOIN testagram_local.accounts a ON a.user_id=p.owner_id
		WHERE p.id=$1::uuid AND p.is_active AND a.account_status='active'
	`, productID).Scan(
		&ownerID,&name,&description,&category,&price,&currency,
		&imageURL,&externalLink,&rating,&reviews,&views,&sales,&featured,&created,
		&username,&displayName,&avatarKey,&verifiedTier,
	)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	profile := map[string]any{
		"id": ownerID, "username": username, "display_name": displayName,
		"avatar_url": avatarKey, "verified_tier": verifiedTier,
	}
	product := map[string]any{
		"id": productID, "owner_id": ownerID, "user_id": ownerID,
		"name": name, "description": description, "category": category,
		"price": price, "currency": currency, "image_url": imageURL,
		"external_link": externalLink, "avg_rating": rating,
		"review_count": reviews, "views_count": views, "sales_count": sales,
		"is_featured": featured, "created_at": created, "user_profiles": profile,
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": product, "error": nil, "request_id": id})
}
