// Opt-in tracing for diagnosing hangs in the capture path: RECORDO_TRACE=1.
#ifndef RECORDO_TRACE_H
#define RECORDO_TRACE_H
#include <stdio.h>
#include <stdlib.h>
#include <pthread.h>
#include <mach/mach_time.h>

static inline int rc_tracing(void) {
	static int on = -1;
	if (on < 0) on = getenv("RECORDO_TRACE") != NULL;
	return on;
}

#define RC_TRACE(...)                                                                  \
	do {                                                                               \
		if (rc_tracing()) {                                                            \
			uint64_t tid;                                                              \
			pthread_threadid_np(NULL, &tid);                                           \
			fprintf(stderr, "  [trace %llu t%llu] ", mach_absolute_time() / 24000ULL,   \
			        (unsigned long long)tid);                                          \
			fprintf(stderr, __VA_ARGS__);                                              \
			fputc('\n', stderr);                                                       \
		}                                                                              \
	} while (0)
#endif
