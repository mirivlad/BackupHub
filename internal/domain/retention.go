package domain

import (
 "errors"
 "fmt"
 "sort"
 "time"
)

// Retention is a dry-run plan only. Deletion/prune requires a separate privileged executor.
type Point struct {
 ID string
 At time.Time
 Verified bool
 Held bool
}
type RetentionPolicy struct {
 KeepLast int
 DailyDays int
 WeeklyWeeks int
 MonthlyMonths int
 MinAge time.Duration
}
type RetentionPlan struct {
 Keep []string `json:"keep"`
 Delete []string `json:"delete"`
}

func PlanRetention(points []Point, policy RetentionPolicy, now time.Time) (RetentionPlan,error) {
 var plan RetentionPlan
 if now.IsZero() { return plan,errors.New("now is required") }
 if policy.KeepLast<0||policy.DailyDays<0||policy.WeeklyWeeks<0||policy.MonthlyMonths<0||policy.MinAge<0 { return plan,errors.New("negative retention policy") }
 sorted:=append([]Point(nil),points...)
 sort.Slice(sorted,func(i,j int)bool { if sorted[i].At.Equal(sorted[j].At){ return sorted[i].ID<sorted[j].ID };return sorted[i].At.After(sorted[j].At) })
 seen:=map[string]bool{}
 keep:=map[string]bool{}
 daily:=map[string]bool{}
 weekly:=map[string]bool{}
 monthly:=map[string]bool{}
 newestVerified:=""
 for i,p:=range sorted {
  if p.ID==""||p.At.IsZero()||p.At.After(now) { return plan,fmt.Errorf("invalid point %q",p.ID) }
  if seen[p.ID] { return plan,fmt.Errorf("duplicate point %q",p.ID) }
  seen[p.ID]=true
  if p.Verified && newestVerified=="" { newestVerified=p.ID }
  if p.Held || i<policy.KeepLast || now.Sub(p.At)<policy.MinAge { keep[p.ID]=true }
  day:=p.At.UTC().Format("2006-01-02")
  yr,week:=p.At.UTC().ISOWeek()
  wk:=fmt.Sprintf("%04d-W%02d",yr,week)
  mon:=p.At.UTC().Format("2006-01")
  if policy.DailyDays>0 && now.Sub(p.At)<time.Duration(policy.DailyDays)*24*time.Hour && !daily[day] {keep[p.ID]=true;daily[day]=true}
  if policy.WeeklyWeeks>0 && now.Sub(p.At)<time.Duration(policy.WeeklyWeeks)*7*24*time.Hour && !weekly[wk] {keep[p.ID]=true;weekly[wk]=true}
  if policy.MonthlyMonths>0 {
   monthAge:=(now.UTC().Year()-p.At.UTC().Year())*12+int(now.UTC().Month())-int(p.At.UTC().Month())
   if monthAge>=0 && monthAge<policy.MonthlyMonths && !monthly[mon] {keep[p.ID]=true;monthly[mon]=true}
  }
 }
 if newestVerified!="" {keep[newestVerified]=true}
 // The newest snapshot is kept even if all other policy fields are zero.
 if len(sorted)>0 {keep[sorted[0].ID]=true}
 for _,p:=range sorted {if keep[p.ID]{plan.Keep=append(plan.Keep,p.ID)}else{plan.Delete=append(plan.Delete,p.ID)}}
 return plan,nil
}
