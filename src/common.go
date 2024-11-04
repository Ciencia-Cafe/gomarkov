package main

import (
	"fmt"
	"runtime/debug"
	"strconv"
)

func Append2[T any](slice *[]T, elems ...T) {
	*slice = append(*slice, elems...)
}

func SnowflakeToUint64(snowflake string) uint64 {
	result, err := strconv.ParseUint(snowflake, 10, 64)
	if err != nil {
		Error(err)
		return 0
	}
	return result
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
