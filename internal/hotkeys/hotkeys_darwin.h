#ifndef SONORYX_HOTKEYS_DARWIN_H
#define SONORYX_HOTKEYS_DARWIN_H

#include <stdint.h>

#define SRX_MAX_HOTKEYS 16

int srxHotkeysApply(const uint32_t *codes, const uint32_t *modifiers, int count, int32_t *status, int timeoutMS);

#endif
