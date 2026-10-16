package main

import (
	"os"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func writeFile(path string) error { return os.WriteFile(path, nil, 0o600) }

func redactorForTest() safety.Redactor { return safety.NewRedactor() }
