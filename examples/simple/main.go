package main

import (
	"fmt"
	"log"

	"go.chrisrx.dev/x/context"

	"go.chrisrx.dev/pywasi"
)

func main() {
	ctx := context.Shutdown()
	defer ctx.Close()

	sysname, err := pywasi.Call[string](ctx, pywasi.Func{
		Module: "mod",
		Name:   "sysname",
	}, pywasi.WithModule("mod", `
			import os

			def sysname() -> str:
				return os.uname().sysname
	`))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("sysname: %v\n", sysname)

	result, err := pywasi.Eval[string](ctx, `
			import mod
			f"Hello from {mod.sysname()}"
	`, pywasi.WithModule("mod", `
			import os

			def sysname() -> str:
				return os.uname().sysname
	`))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%v!!!!!!\n", result)
}
