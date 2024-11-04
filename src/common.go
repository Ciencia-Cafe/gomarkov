package main

import (
	"fmt"
	"runtime/debug"
)

func Append2[T any](slice *[]T, elems ...T) {
	*slice = append(*slice, elems...)
}

func Info(args ...any) {
	args = append([]any{"[INFO]"}, args...)
	fmt.Println(args...)
}

func Warn(args ...any) {
	args = append([]any{"[WARN]"}, args...)
	fmt.Println(args...)
}

func Error(args ...any) {
	args = append([]any{"[ERROR]"}, args...)
	args = append(args, "\n"+string(debug.Stack()))
	fmt.Println(args...)
}
