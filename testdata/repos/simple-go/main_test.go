package main

import "testing"

func TestLogin(t *testing.T) {
	service := AuthService{}
	if err := service.Login("demo"); err != nil {
		t.Fatal(err)
	}
}
