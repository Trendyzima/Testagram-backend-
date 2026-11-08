package main

import (
 "net/http"
 "net/http/httptest"
 "testing"
)

func TestNativeDiscoveryAndFeatureRoutesFailClosedWithoutDatabase(t *testing.T) {
 handler:=(&service{}).routes()
 paths:=[]string{
  "/api/discovery/creators",
  "/api/discovery/posts",
  "/api/discovery/threads",
  "/api/discovery/post-counts?terms=news,update",
  "/api/marketplace/products",
  "/api/stories/explore",
  "/api/hashtags",
  "/api/hashtag-challenges",
  "/api/communities",
 }
 for _,path:=range paths {
  t.Run(path,func(t *testing.T){
   req:=httptest.NewRequest(http.MethodGet,path,nil)
   req.Header.Set("Origin","https://testagram.site")
   rec:=httptest.NewRecorder()
   handler.ServeHTTP(rec,req)
   if rec.Code!=http.StatusServiceUnavailable {
    t.Fatalf("status=%d want=%d body=%s",rec.Code,http.StatusServiceUnavailable,rec.Body.String())
   }
  })
 }
}
