package main

import (
	"fmt"
	"os"

	"example.com/fixture/calc"
	"example.com/fixture/clean"
)

func main() {
	f, _ := os.Create(os.DevNull)
	f.Close() // errcheck: error return value not checked
	q, err := calc.Div(10, 2)
	fmt.Printf("%d\n", q, err) // govet printf: too many arguments
	fmt.Println(calc.Grade(71, true, false), clean.Add(1, 2))
}
