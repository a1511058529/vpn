package main

import "os"

func osStat(p string) (os.FileMode, error) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return st.Mode().Perm(), nil
}
