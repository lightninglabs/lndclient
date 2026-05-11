package lndclient

import (
	"encoding/hex"
	"errors"
	"image/color"
	"regexp"
)

var (
	validColorRegexp = regexp.MustCompile("^#[A-Fa-f0-9]{6}$")
)

// ParseHexColor takes a hex string representation of a color in the form
// "#RRGGBB", parses the hex color values, and returns a color.RGBA struct of
// the same color.
func ParseHexColor(colorStr string) (color.RGBA, error) {
	if !validColorRegexp.MatchString(colorStr) {
		return color.RGBA{}, errors.New("color must be specified " +
			"using a hexadecimal value in the form #RRGGBB")
	}

	colorBytes, err := hex.DecodeString(colorStr[1:])
	if err != nil {
		return color.RGBA{}, err
	}

	return color.RGBA{
		R: colorBytes[0],
		G: colorBytes[1],
		B: colorBytes[2],
	}, nil
}
