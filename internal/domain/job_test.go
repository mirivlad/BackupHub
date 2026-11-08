package domain

import "testing"

func TestJobTransitions(t *testing.T) {
 valid:=[][2]JobState{{JobScheduled,JobQueued},{JobQueued,JobPreflight},{JobPreflight,JobPreparing},{JobPreparing,JobCapturing},{JobCapturing,JobCommitting},{JobCommitting,JobVerifying},{JobVerifying,JobCompleted},{JobWaiting,JobQueued}}
 for _,p:=range valid {if err:=ValidateTransition(p[0],p[1]);err!=nil{t.Error(err)}}
 invalid:=[][2]JobState{{JobCapturing,JobCompleted},{JobFailed,JobQueued},{JobCompleted,JobScheduled},{JobVerifying,JobCommitting},{JobScheduled,JobScheduled}}
 for _,p:=range invalid {if err:=ValidateTransition(p[0],p[1]);err==nil{t.Errorf("accepted %s -> %s",p[0],p[1])}}
}
