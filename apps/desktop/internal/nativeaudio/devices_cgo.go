package nativeaudio

/*
#include <stdlib.h>
*/
import "C"

import "unsafe"

func freeDeviceID(pointer unsafe.Pointer) {
	C.free(pointer)
}
