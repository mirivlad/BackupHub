package httpapi

import (
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "testing"
 "github.com/mirivlad/BackupHub/internal/catalog"
)

func TestHealthShowsNotReady(t *testing.T) {
 db,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)}
 defer db.Close()
 req:=httptest.NewRequest(http.MethodGet,"/api/v1/health",nil)
 res:=httptest.NewRecorder()
 New(db).ServeHTTP(res,req)
 if res.Code!=http.StatusOK{t.Fatalf("status %d",res.Code)}
 var body struct{BackupsAvailable bool `json:"backups_available"`}
 if err:=json.Unmarshal(res.Body.Bytes(),&body);err!=nil{t.Fatal(err)}
 if body.BackupsAvailable{t.Fatal("unimplemented backups reported available")}
}
func TestHealthReadOnly(t *testing.T){
 db,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)}
 defer db.Close()
 req:=httptest.NewRequest(http.MethodPost,"/api/v1/health",nil)
 res:=httptest.NewRecorder();New(db).ServeHTTP(res,req)
 if res.Code!=http.StatusMethodNotAllowed{t.Fatalf("expected 405, got %d",res.Code)}
}
