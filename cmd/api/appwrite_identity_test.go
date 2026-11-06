package main

import "testing"

func TestValidateAppwriteProvisionInput(t *testing.T) {
  tests := []struct{name string; in appwriteProvisionInput; wantErr bool}{
    {"valid",appwriteProvisionInput{Username:"trendy_kenya",DisplayName:"Trendy"},false},
    {"short username",appwriteProvisionInput{Username:"ab",DisplayName:"Trendy"},true},
    {"unsafe username",appwriteProvisionInput{Username:"../admin",DisplayName:"Trendy"},true},
    {"empty display name",appwriteProvisionInput{Username:"trendy_kenya"},true},
    {"long display name",appwriteProvisionInput{Username:"trendy_kenya",DisplayName:string(make([]byte,81))},true},
  }
  for _,tt:=range tests { t.Run(tt.name,func(t *testing.T){ err:=validateAppwriteProvisionInput(tt.in); if (err!=nil)!=tt.wantErr { t.Fatalf("error=%v wantErr=%v",err,tt.wantErr) } }) }
}
