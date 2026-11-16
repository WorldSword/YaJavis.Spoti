/*
 * vqe_tap.so — копия обработанного микрофона Станции для jarvisd.
 *
 * Загружается в maind --platformd через LD_PRELOAD. Перехватывает YandexVqeC_GetOutputChannelData
 * (выход Yandex VQE), вызывает оригинал и, если это канал vqe_0 (тип 6, индекс 0, моно 16 кГц,
 * float32 в диапазоне [-1, 1]), отправляет копию в Unix-сокет JARVIS_MIC_SOCKET
 * (или ALICE_CAPTURE_SOCKET) датаграммами по 10 мс s16le.
 *
 * Правила: оригинальный результат, буфер и errno не меняются; отправка неблокирующая —
 * если jarvisd не запущен или не успевает, штатная Алиса этого не замечает.
 *
 * Сборка: zig cc -target arm-linux-gnueabihf.2.30 -shared -fPIC -O2 -o vqe_tap.so vqe_tap.c -ldl
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

typedef void *(*vqe_output_fn)(void *, unsigned int, unsigned int, const float **, size_t *);

static vqe_output_fn real_output;
static int sock = -1;
static int ready;
static struct sockaddr_un dest;
static socklen_t dest_len;
static float gain = 1.0f;
static unsigned int want_kind = 6, want_index = 0; /* JARVIS_MIC_CHANNEL="6:0" — другой канал на другой прошивке */
static char seen_path[sizeof(((struct sockaddr_un *)0)->sun_path) + 8];
static unsigned char seen[16][8];

/* note_channel — какие каналы VQE вообще приходят: по строке на каждую новую пару в <сокет>.seen.
 * Пишется редко (только новая пара, их единицы) — для самодиагностики jarvisd на незнакомых прошивках. */
static void note_channel(unsigned int kind, unsigned int index, size_t frames) {
    if (!seen_path[0] || kind >= 16 || index >= 8 || seen[kind][index]) return;
    seen[kind][index] = 1;
    int fd = open(seen_path, O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC, 0644);
    if (fd < 0) return;
    char line[64];
    int n = snprintf(line, sizeof line, "%u %u %u\n", kind, index, (unsigned int)frames);
    if (n > 0) (void)write(fd, line, (size_t)n);
    close(fd);
}

static void setup(void) {
    ready = 1;
    const char *path = getenv("JARVIS_MIC_SOCKET");
    if (!path || !*path) path = getenv("ALICE_CAPTURE_SOCKET");
    if (!path || !*path || strlen(path) >= sizeof dest.sun_path) return;
    const char *ch = getenv("JARVIS_MIC_CHANNEL");
    if (ch && *ch) {
        unsigned int k, i;
        if (sscanf(ch, "%u:%u", &k, &i) == 2) want_kind = k, want_index = i;
    }
    snprintf(seen_path, sizeof seen_path, "%s.seen", path);
    unlink(seen_path);
    const char *g = getenv("JARVIS_MIC_GAIN");
    if (g && *g) {
        float v = strtof(g, NULL);
        if (v > 0.05f && v < 20.0f) gain = v;
    }
    memset(&dest, 0, sizeof dest);
    dest.sun_family = AF_UNIX;
    strcpy(dest.sun_path, path);
    dest_len = (socklen_t)(offsetof(struct sockaddr_un, sun_path) + strlen(path) + 1);
    sock = socket(AF_UNIX, SOCK_DGRAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
}

static short to_s16(float x) {
    if (x != x) return 0; /* NaN */
    x *= gain;
    if (x >= 1.0f) return 32767;
    if (x <= -1.0f) return -32768;
    return (short)(x * 32768.0f);
}

void *YandexVqeC_GetOutputChannelData(void *engine, unsigned int kind, unsigned int index,
                                      const float **data, size_t *frames) {
    int saved = errno;
    if (!real_output) {
        real_output = (vqe_output_fn)dlsym(RTLD_NEXT, "YandexVqeC_GetOutputChannelData");
        if (!real_output) _exit(2); /* без оригинала maind работать не может */
    }
    errno = saved;
    void *err = real_output(engine, kind, index, data, frames);
    saved = errno;

    if (!err && data && *data && frames && *frames > 0 && *frames <= 4096) {
        if (!ready) setup();
        note_channel(kind, index, *frames);
    }
    if (!err && kind == want_kind && index == want_index && data && *data && frames && *frames > 0 && *frames <= 4096) {
        if (sock >= 0) {
            short pkt[160]; /* 10 мс моно 16 кГц */
            size_t n = *frames;
            for (size_t off = 0; off < n;) {
                size_t c = n - off < 160 ? n - off : 160;
                for (size_t i = 0; i < c; i++) pkt[i] = to_s16((*data)[off + i]);
                (void)sendto(sock, pkt, c * sizeof(short), MSG_DONTWAIT | MSG_NOSIGNAL,
                             (struct sockaddr *)&dest, dest_len);
                off += c;
            }
        }
    }
    errno = saved;
    return err;
}
