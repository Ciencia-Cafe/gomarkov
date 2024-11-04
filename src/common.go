package main

import "fmt"

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
	fmt.Println(args...)
}
