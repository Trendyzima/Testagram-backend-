package main

import (
 "net/http"
 "strconv"
 "strings"
 "time"
)

func (s *service) marketplaceProductsList(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MARKETPLACE_UNAVAILABLE","Marketplace is unavailable",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 category:=strings.TrimSpace(r.URL.Query().Get("category"));if category=="all"{category=""};if len(category)>48{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MARKETPLACE_CATEGORY","Category is invalid",id));return}
 q:=strings.TrimSpace(r.URL.Query().Get("q"));if len(q)>160{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MARKETPLACE_QUERY","Product search is too long",id));return}
 rows,err:=s.db.Query(r.Context(),`SELECT p.id::text,p.owner_id::text,p.name,p.description,p.category,p.price::text,p.image_url,p.external_link,p.avg_rating,p.review_count,p.views_count,p.sales_count,p.is_featured,p.created_at,a.username,a.display_name,a.avatar_key,a.verified_tier
 FROM testagram_local.marketplace_products p JOIN testagram_local.accounts a ON a.user_id=p.owner_id
 WHERE p.is_active AND ($1='' OR p.category=$1) AND ($2='' OR p.name ILIKE '%'||$2||'%' OR p.description ILIKE '%'||$2||'%')
 ORDER BY p.is_featured DESC,p.views_count DESC,p.sales_count DESC,p.created_at DESC LIMIT $3`,category,q,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MARKETPLACE_PRODUCTS_FAILED","Marketplace products could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0);for rows.Next(){var pid,owner,name,description,category,price,image,external,username,display,avatar,tier string;var rating float64;var reviews,views,sales int64;var featured bool;var created time.Time
 if err=rows.Scan(&pid,&owner,&name,&description,&category,&price,&image,&external,&rating,&reviews,&views,&sales,&featured,&created,&username,&display,&avatar,&tier);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MARKETPLACE_PRODUCTS_FAILED","Marketplace product records could not be read",id));return}
 profile:=map[string]any{"id":owner,"username":username,"display_name":display,"avatar_url":avatar,"verified_tier":tier}
 items=append(items,map[string]any{"id":pid,"user_id":owner,"owner_id":owner,"name":name,"description":description,"category":category,"price":price,"image_url":image,"external_link":external,"avg_rating":rating,"review_count":reviews,"views_count":views,"sales_count":sales,"is_featured":featured,"created_at":created,"user_profiles":profile})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MARKETPLACE_PRODUCTS_FAILED","Marketplace product records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}
