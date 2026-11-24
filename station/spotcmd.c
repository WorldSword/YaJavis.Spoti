/*
 * spotcmd — встроенный распознаватель коротких команд Станции («стоп», «хватит», «громче», «тише»…)
 * отдельным процессом для jarvisd.
 *
 * Читает со stdin звук: моно, 16 кГц, s16le (тот же обработанный микрофон, что идёт в jarvisd).
 * Каждое услышанное слово пишет в stdout строкой "HIT <фраза>". Сообщения о работе — в stderr.
 * Модель и библиотека — штатные файлы прошивки: libyandex_evil.so и spotter_models/command.
 * Отдельный процесс, а не код внутри maind: если что-то пойдёт не так, Алиса этого не заметит.
 *
 * Прототипы функций библиотеки описаны в открытой документации проекта pulsar; код здесь свой.
 *
 * Сборка (как и maind, 32-битный ARM):
 *   zig cc -target arm-linux-gnueabihf.2.30 -mcpu=cortex_a7 -O2 -s -o spotcmd spotcmd.c -ldl
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#define LIBS "/system/vendor/quasar/libs/"
#define MODEL "/system/vendor/quasar/spotter_models/command"
#define TYPES 6
#define MAXCH 16

typedef void *P;
static P (*conf_read)(const char *, P *);
static int (*conf_rate)(P, P *);
static long (*conf_chans)(P, long, P *);
static P (*sp_create)(P, int, int, const int *, bool, P *);
static void (*sp_submit)(P, const void *, size_t, P *);
static int (*sp_next)(P, P *);
static const char *(*sp_phrase)(P, P *);
static const char *(*err_msg)(P);
static void (*err_free)(P);

static int failed(const char *what, P err) {
    if (!err) return 0;
    const char *m = err_msg ? err_msg(err) : NULL;
    fprintf(stderr, "spotcmd: %s: %s\n", what, m ? m : "?");
    if (err_free) err_free(err);
    return 1;
}

#define SYM(v, name)                                                    \
    do {                                                                \
        *(void **)&v = dlsym(lib, name);                                \
        if (!v) {                                                       \
            fprintf(stderr, "spotcmd: нет функции %s\n", name);         \
            return 2;                                                   \
        }                                                               \
    } while (0)

static long long now_ms(void) {
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return (long long)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}

int main(int argc, char **argv) {
    const char *model = argc > 1 ? argv[1] : MODEL;
    setvbuf(stdout, NULL, _IOLBF, 0);
    /* зависимости библиотеки лежат не на стандартном пути — грузим их заранее и глобально */
    dlopen(LIBS "libiconv.so", RTLD_NOW | RTLD_GLOBAL);
    if (!dlopen(LIBS "libtflite.so", RTLD_NOW | RTLD_GLOBAL))
        fprintf(stderr, "spotcmd: libtflite: %s\n", dlerror());
    void *lib = dlopen(LIBS "libyandex_evil.so", RTLD_NOW | RTLD_GLOBAL);
    if (!lib) {
        fprintf(stderr, "spotcmd: %s\n", dlerror());
        return 2;
    }
    SYM(conf_read, "spotter_conf_read");
    SYM(conf_rate, "spotter_conf_native_sample_rate");
    SYM(conf_chans, "spotter_conf_get_required_channel_count");
    SYM(sp_create, "spotter_create_multichannel");
    SYM(sp_submit, "spotter_submit_raw_data");
    SYM(sp_next, "spotter_get_next_event");
    SYM(sp_phrase, "spotter_get_current_phrase");
    SYM(err_msg, "spotter_error_get_message");
    SYM(err_free, "spotter_error_free");

    P err = NULL;
    P conf = conf_read(model, &err);
    if (failed("модель", err) || !conf) return 3;
    err = NULL;
    int rate = conf_rate(conf, &err);
    failed("частота", err);

    /* у нас один канал — обработанный микрофон; им заполняем все каналы, которые просит модель */
    int types[MAXCH], n = 0;
    for (int t = 0; t < TYPES; t++) {
        err = NULL;
        long c = conf_chans(conf, t, &err);
        if (failed("каналы", err)) continue;
        for (long i = 0; i < c && c < 8 && n < MAXCH; i++) types[n++] = t;
    }
    if (n == 0) types[n++] = 0;
    err = NULL;
    P sp = sp_create(conf, 16000, n, types, false, &err);
    if (failed("создание", err) || !sp) return 4;
    fprintf(stderr, "spotcmd: готов (модель %d Гц, каналов %d)\n", rate, n);

    short in[160], out[160 * MAXCH];
    long long last = 0;
    for (;;) {
        size_t got = fread(in, sizeof(short), 160, stdin);
        if (got == 0) break;
        for (size_t i = 0; i < got; i++)
            for (int c = 0; c < n; c++) out[i * n + c] = in[i];
        err = NULL;
        sp_submit(sp, out, got * n * sizeof(short), &err);
        if (failed("звук", err)) continue;
        for (int k = 0; k < 16; k++) {
            err = NULL;
            int ev = sp_next(sp, &err);
            if (failed("событие", err) || ev < 0 || ev > 9 || ev == 0 || ev == 4 || ev == 7 || ev == 9) break;
            if (ev != 2 && ev != 5) continue;
            err = NULL;
            const char *ph = sp_phrase(sp, &err);
            failed("фраза", err);
            long long t = now_ms();
            if (t - last < 1200) continue; /* одно слово — одно срабатывание */
            last = t;
            printf("HIT %s\n", ph ? ph : "");
        }
    }
    return 0;
}
