package main

import "github.com/alpha-omega-security/hyrum/internal/terminal"

func safeLine(s string) string { return terminal.SingleLine(s) }
func safeText(s string) string { return terminal.Multiline(s) }
