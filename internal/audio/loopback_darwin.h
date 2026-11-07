#ifndef SONORYX_LOOPBACK_DARWIN_H
#define SONORYX_LOOPBACK_DARWIN_H

#include <stdint.h>

typedef struct {
	uint32_t tap;
	uint32_t aggregate;
	void *proc;
	double rate;
	int status;
	char message[160];
} srxTapResult;

typedef struct {
	int32_t pid;
	char name[128];
} srxApp;

int srxTapAvailable(void);
void srxTapStart(int32_t pid, int exclude, uintptr_t handle, srxTapResult *result);
void srxTapStop(uint32_t tap, uint32_t aggregate, void *proc);
int srxListApps(srxApp *apps, int capacity);
void *srxWatchDefaultOutput(uintptr_t id);
void srxUnwatchDefaultOutput(void *token);

#endif
