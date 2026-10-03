package main

import "os"

func openDevNull() (*os.File, error) { return os.OpenFile(os.DevNull, os.O_RDWR, 0) }
