package main

func Append2[T any](slice *[]T, elems ...T) {
	*slice = append(*slice, elems...)
}
