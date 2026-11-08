package domain

import ("testing";"time")

func TestRetentionProtectsHoldAndLastVerified(t *testing.T) {
 now:=time.Date(2026,11,8,0,0,0,0,time.UTC)
 points:=[]Point{
  {ID:"new-broken",At:now.Add(-time.Hour)},
  {ID:"last-good",At:now.Add(-24*time.Hour),Verified:true},
  {ID:"held",At:now.Add(-48*time.Hour),Held:true},
  {ID:"old",At:now.Add(-72*time.Hour)},
 }
 result,err:=PlanRetention(points,RetentionPolicy{KeepLast:1},now)
 if err!=nil{t.Fatal(err)}
 got:=map[string]bool{};for _,id:=range result.Keep{got[id]=true}
 if !got["last-good"]||!got["held"]||!got["new-broken"]{t.Fatalf("lost protected snapshots: %+v",result)}
 if len(result.Delete)!=1||result.Delete[0]!="old"{t.Fatalf("unexpected deletion: %+v",result)}
}
func TestRetentionRejectsDuplicate(t *testing.T) {
 now:=time.Now().UTC()
 _,err:=PlanRetention([]Point{{ID:"x",At:now},{ID:"x",At:now}},RetentionPolicy{},now)
 if err==nil{t.Fatal("duplicate accepted")}
}
