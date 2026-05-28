package main

import "fmt"

type AuthService struct{}

func (a AuthService) Login(user string) error {
	fmt.Println(user)
	return nil
}

func main() {
	service := AuthService{}
	_ = service.Login("demo")
}
