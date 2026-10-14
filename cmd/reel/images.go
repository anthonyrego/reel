package main

import "github.com/anthonyrego/reel/internal/draw"

// The image ids the windows draw pictures with (each set on the renderer
// with SetImage by whatever owns its texture).
const (
	// areaImage is the area picker's still of the display.
	areaImage draw.ImageID = 1
	// trimImage is the trim screen's picture; trimStrip its filmstrip.
	trimImage draw.ImageID = 2
	trimStrip draw.ImageID = 3
	// cameraImage is the camera bubble's picture.
	cameraImage draw.ImageID = 4
	// thumbBase is the first of the recordings' pictures (see thumbs).
	thumbBase draw.ImageID = 1000
)
