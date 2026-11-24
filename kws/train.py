"""Обучение детектора «Джарвис» для jarvisd (потоковая причинная свёрточная сеть).

Признаки и сеть совпадают один в один с jarvisd/kws.go:
  окно Ханна 400 (25 мс), шаг 160 (10 мс), FFT 512, 40 мел-полос HTK 20–7600 Гц, log(x+1e-6), нормировка mean/std;
  proj 1x1 (40→C) + ReLU; блоки: x += ReLU(causal_conv3(x, dilation)); head 1x1 (C→1) → логит на каждый кадр.
Обучение — MIL: у «Джарвис»-клипа максимум логита по кадрам → 1, у остальных → 0 (кадры прогрева не считаются).

  python train.py --rec <папка записей> --synth data/synth --out out
"""
import argparse, glob, json, math, os, random, struct, time, wave
import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F
from scipy.signal import resample_poly, fftconvolve

SR, WIN, HOP, NFFT, NMELS, FMIN, FMAX = 16000, 400, 160, 512, 40, 20.0, 7600.0
CH, DIL, K = 48, [1, 2, 4, 8, 16, 32], 3
CLIP = int(3.5 * SR)      # длина обучающего клипа
WARM = 100                # первые 1.0 с — прогрев, максимум по ним не берём
dev = torch.device("cuda" if torch.cuda.is_available() else "cpu")


# ------------------------------------------------------------------ признаки (как в kws.go)
def hz2mel(f): return 2595 * np.log10(1 + f / 700)
def mel2hz(m): return 700 * (10 ** (m / 2595) - 1)

def mel_bank():
    bins = NFFT // 2 + 1
    pts = mel2hz(np.linspace(hz2mel(FMIN), hz2mel(FMAX), NMELS + 2))
    f = np.arange(bins) * SR / NFFT
    bank = np.zeros((NMELS, bins), np.float32)
    for m in range(NMELS):
        l, c, r = pts[m], pts[m + 1], pts[m + 2]
        up = (f > l) & (f < c)
        down = (f >= c) & (f < r)
        bank[m, up] = (f[up] - l) / (c - l)
        bank[m, down] = (r - f[down]) / (r - c)
    return bank

BANK = torch.tensor(mel_bank(), device=dev)
WINDOW = torch.hann_window(WIN, periodic=True, dtype=torch.float64, device=dev).float()

def logmel(x):  # x: [B, N] float (-1..1) → [B, 40, T]
    fr = x.unfold(1, WIN, HOP) * WINDOW
    spec = torch.fft.rfft(fr, n=NFFT)
    power = spec.real ** 2 + spec.imag ** 2
    return torch.log(power @ BANK.T + 1e-6).transpose(1, 2)


# ------------------------------------------------------------------ сеть
class KWS(nn.Module):
    def __init__(self):
        super().__init__()
        self.register_buffer("mean", torch.zeros(NMELS))
        self.register_buffer("std", torch.ones(NMELS))
        self.proj = nn.Conv1d(NMELS, CH, 1)
        self.blocks = nn.ModuleList(nn.Conv1d(CH, CH, K, dilation=d) for d in DIL)
        self.head = nn.Conv1d(CH, 1, 1)
        self.drop = nn.Dropout(0.1)

    def forward(self, feat):
        x = (feat - self.mean[:, None]) / self.std[:, None]
        x = F.relu(self.proj(x))
        for b, d in zip(self.blocks, DIL):
            x = x + self.drop(F.relu(b(F.pad(x, ((K - 1) * d, 0)))))
        return self.head(x)[:, 0]  # [B, T]


# ------------------------------------------------------------------ данные
def read_wav(p):
    with wave.open(p) as w:
        assert w.getframerate() == SR and w.getsampwidth() == 2
        x = np.frombuffer(w.readframes(w.getnframes()), "<i2").astype(np.float32) / 32768
        if w.getnchannels() > 1:
            x = x.reshape(-1, w.getnchannels()).mean(1)
    return x

def nonzero_runs(x, min_len):
    """Куски записи без вставленной тишины (колонка шлёт звук только когда громко)."""
    z = np.abs(x) < 1e-6
    hop = 320
    n = len(x) // hop
    silent = z[: n * hop].reshape(n, hop).all(1)
    runs, start = [], None
    for i, s in enumerate(silent):
        if not s and start is None:
            start = i
        if (s or i == n - 1) and start is not None:
            end = i if s else n
            if (end - start) * hop >= min_len:
                runs.append(x[start * hop:end * hop])
            start = None
    return runs

def load_recordings(rec):
    pos, neg_look, bg = [], [], []
    for jf in sorted(glob.glob(os.path.join(rec, "*.json"))):
        meta = json.load(open(jf, encoding="utf-8"))
        x = read_wav(jf[:-5] + ".wav")
        kind = meta.get("kind")
        if kind == "background":
            bg.append(x)
            continue
        floor = np.percentile(20 * np.log10(np.abs(x[np.abs(x) > 1e-6]) + 1e-9), 15)
        for e in meta["events"]:
            if e["type"] != "prompt":
                continue
            t = e["t"]
            a, b = int((t - 1.2) * SR), int((t + 2.3) * SR)
            if a < 0 or b > len(x):
                continue
            seg = x[a:b]
            word = seg[int(1.0 * SR):]
            peak = 20 * np.log10(np.abs(word).max() + 1e-9)
            if peak < floor + 25:  # подсказку пропустили — слова нет
                continue
            (pos if kind == "jarvis" else neg_look).append((seg, e["block"]))
    return pos, neg_look, bg

def load_synth(root):
    pos, neg = [], []
    for kind, lst in (("pos", pos), ("neg", neg)):
        for p in glob.glob(os.path.join(root, "*", kind, "*.wav")):
            x = read_wav(p)
            if len(x) > 0.2 * SR and np.abs(x).max() > 0.01:
                lst.append(x[: int(2.4 * SR)])
    return pos, neg


class Noise:
    """Фон комнаты из записей (через ту же обработку колонки) для подмешивания."""
    def __init__(self, bgs):
        runs = [r for b in bgs for r in nonzero_runs(b, SR)]
        self.audio = np.concatenate(runs) if runs else np.zeros(SR * 10, np.float32)

    def take(self, n):
        i = random.randint(0, max(0, len(self.audio) - n - 1))
        out = self.audio[i:i + n]
        return np.pad(out, (0, n - len(out)))


def reverb(x):
    rt = random.uniform(0.15, 0.6)
    n = int(rt * SR)
    ir = np.random.randn(n).astype(np.float32) * np.exp(-6.9 * np.arange(n) / n).astype(np.float32)
    ir[0] = 1.0
    ir /= np.sqrt((ir ** 2).sum())
    y = fftconvolve(x, ir)[: len(x)]
    return y.astype(np.float32)

def speed(x):
    r = random.choice([(4, 5), (9, 10), (19, 20), (21, 20), (11, 10), (5, 4), (4, 3)])  # до ±25%: «Джаааарвис» и скороговоркой
    return resample_poly(x, r[0], r[1]).astype(np.float32)

def rms(x): return float(np.sqrt((x ** 2).mean() + 1e-12))

def place(word, noise, at=None):
    """Положить фразу в клип 3.5 с: слово после прогрева, вокруг — фон комнаты."""
    clip = noise.take(CLIP) * random.uniform(0.3, 1.5)
    if at is None:
        at = random.randint(int(1.05 * SR), max(int(1.05 * SR), CLIP - len(word) - int(0.2 * SR)))
    w = word[: CLIP - at]
    snr = random.uniform(3, 30)
    g = rms(w) / (rms(clip[at:at + len(w)]) * 10 ** (snr / 20) + 1e-9)
    clip = clip / max(g, 1e-3) if g < 1 else clip
    clip[at:at + len(w)] += w
    return clip

def augment_real(seg, noise):
    x = seg.copy()
    if random.random() < 0.3:
        x = speed(x)
        x = np.pad(x, (0, max(0, CLIP - len(x))))[:CLIP]
    if random.random() < 0.5:
        x = x + noise.take(len(x)) * (rms(x) / (rms(noise.take(len(x))) + 1e-9)) * 10 ** (-random.uniform(8, 30) / 20)
    shift = random.randint(-int(0.15 * SR), int(0.15 * SR))
    x = np.roll(x, shift)
    return x * 10 ** (random.uniform(-8, 6) / 20)

def augment_synth(w, noise):
    if random.random() < 0.4:
        w = speed(w)
    if random.random() < 0.6:
        w = reverb(w)
    w = w / (np.abs(w).max() + 1e-9) * random.uniform(0.05, 0.6)
    return place(w, noise)

def finalize(x):
    x = np.asarray(x, np.float32)[:CLIP]
    x = np.pad(x, (0, CLIP - len(x)))
    peak = np.abs(x).max()
    return x / peak * 0.95 if peak > 0.99 else x


# ------------------------------------------------------------------ потоковая оценка (как jarvisd)
@torch.no_grad()
def stream_probs(model, x):
    model.eval()
    feats = logmel(torch.tensor(x[None], device=dev))
    p = torch.sigmoid(model(feats))[0].cpu().numpy()
    return np.convolve(p, np.ones(3) / 3)[: len(p)]  # сглаживание за 3 кадра

def count_hits(p, thr, cooldown=150, start=0):
    hits, i = 0, start
    while i < len(p):
        if p[i] >= thr:
            hits += 1
            i += cooldown
        else:
            i += 1
    return hits


def main():
    import sys
    sys.stdout.reconfigure(encoding="utf-8")
    ap = argparse.ArgumentParser()
    ap.add_argument("--rec", required=True)
    ap.add_argument("--synth", required=True)
    ap.add_argument("--out", default="out")
    ap.add_argument("--steps", type=int, default=6000)
    ap.add_argument("--batch", type=int, default=128)
    ap.add_argument("--pool", type=int, default=16000)
    ap.add_argument("--hard", default="", help="папка ложных срабатываний с колонки (*.wav) — жёсткие негативы")
    ap.add_argument("--pos", default="", help="папка настоящих «Джарвис» с колонки (*.wav, слово в конце клипа) — живые позитивы")
    ap.add_argument("--all", action="store_true", help="учить на всех записях (без отложенных)")
    ap.add_argument("--bg", default="", help="папка фона с колонки (*.wav из режима обучения) — добавляется к фону комнаты")
    ap.add_argument("--thresholds", default="", help="10 порогов через запятую вместо подбора")
    args = ap.parse_args()
    random.seed(1); np.random.seed(1); torch.manual_seed(1)
    os.makedirs(args.out, exist_ok=True)

    t0 = time.time()
    pos_real, neg_look, bgs = load_recordings(args.rec)
    if args.bg:
        bgs = bgs + [read_wav(p) for p in glob.glob(os.path.join(args.bg, "*.wav"))]
    syn_pos, syn_neg = load_synth(args.synth)
    # отложенные для проверки: блоки 2 и 7 «Джарвис», блок 3 похожих слов, последние 25% фона
    VAL_POS_BLOCKS, VAL_LOOK_BLOCK = ({2, 7}, 3) if not args.all else (set(), -1)
    tr_pos = [s for s, b in pos_real if b not in VAL_POS_BLOCKS]
    va_pos = [s for s, b in pos_real if b in VAL_POS_BLOCKS]
    tr_look = [s for s, b in neg_look if b != VAL_LOOK_BLOCK]
    va_look = [s for s, b in neg_look if b == VAL_LOOK_BLOCK]
    bg_all = np.concatenate([np.concatenate(nonzero_runs(b, SR)) for b in bgs]) if bgs else np.zeros(SR, np.float32)
    cut = int(len(bg_all) * (0.75 if not args.all else 0.9))
    bg_tr, bg_va = bg_all[:cut], bg_all[cut:]
    noise = Noise([bg_tr])
    hard = [read_wav(p) for p in glob.glob(os.path.join(args.hard, "*.wav"))] if args.hard else []
    hard = [h for h in hard if len(h) > SR]
    live = [read_wav(p) for p in glob.glob(os.path.join(args.pos, "*.wav"))] if args.pos else []
    live = [h for h in live if len(h) > SR]
    print(f"живых «Джарвис» с колонки: {len(live)}")
    print(f"ложных срабатываний для дообучения: {len(hard)}")
    print(f"данные: свои «Джарвис» {len(tr_pos)}+{len(va_pos)} (обуч+пров), похожие {len(tr_look)}+{len(va_look)}, "
          f"синтетика {len(syn_pos)}/{len(syn_neg)}, фон {len(bg_tr)/SR/60:.1f}+{len(bg_va)/SR/60:.1f} мин "
          f"[{time.time()-t0:.0f} с]")

    model = KWS().to(dev)
    # нормировка признаков по фону и своим словам
    sample = [finalize(augment_real(random.choice(tr_pos), noise)) for _ in range(64)] + [finalize(noise.take(CLIP)) for _ in range(64)]
    f = logmel(torch.tensor(np.stack(sample), device=dev))
    model.mean.copy_(f.mean((0, 2)))
    model.std.copy_(f.std((0, 2)).clamp_min(0.1))

    def make_batch(n):
        xs, ys = [], []
        for _ in range(n):
            r = random.random()
            if r < 0.30:
                x, y = augment_real(random.choice(tr_pos), noise), 1
            elif r < 0.50:
                x, y = augment_synth(random.choice(syn_pos), noise), 1
            elif r < 0.53 and live:
                h = random.choice(live)  # настоящий оклик в быту: слово в конце клипа, до него — обычная речь
                x = np.concatenate([noise.take(max(0, CLIP - len(h))) * 0.5, h])[-CLIP:]
                x = x * 10 ** (random.uniform(-8, 6) / 20)
                if random.random() < 0.5:
                    x = x + noise.take(len(x)) * (rms(x) / (rms(noise.take(len(x))) + 1e-9)) * 10 ** (-random.uniform(10, 30) / 20)
                y = 1
            elif r < 0.60 and hard:
                h = random.choice(hard)  # кусок, на котором колонка сработала зря (до 3 с, слово было бы в конце)
                x = np.concatenate([noise.take(max(0, CLIP - len(h))) * 0.5, h])[-CLIP:]
                x, y = augment_real(x, noise), 0
            elif r < 0.65:
                x, y = augment_real(random.choice(tr_look), noise), 0
            elif r < 0.77:
                x, y = augment_synth(random.choice(syn_neg), noise), 0
            else:
                i = random.randint(0, len(bg_tr) - CLIP - 1)
                x, y = bg_tr[i:i + CLIP] * 10 ** (random.uniform(-6, 6) / 20), 0
            xs.append(finalize(x)); ys.append(y)
        return torch.tensor(np.stack(xs), device=dev), torch.tensor(ys, device=dev, dtype=torch.float32)

    # пул заранее аугментированных клипов в памяти видеокарты (float16), дальше — дешёвые искажения на GPU
    xs, ys = [], []
    for i in range(0, args.pool, 512):
        x, y = make_batch(min(512, args.pool - i))
        xs.append(x.half()); ys.append(y)
        if i % 4096 == 0:
            print(f"  готовлю клипы: {i}/{args.pool} [{time.time()-t0:.0f} с]", flush=True)
    PX, PY = torch.cat(xs), torch.cat(ys)
    print(f"пул: {len(PX)} клипов, «Джарвис» {int(PY.sum())} [{time.time()-t0:.0f} с]")

    opt = torch.optim.AdamW(model.parameters(), lr=2e-3, weight_decay=1e-3)
    sched = torch.optim.lr_scheduler.OneCycleLR(opt, max_lr=3e-3, total_steps=args.steps, pct_start=0.1)
    noise_gpu = torch.tensor(noise.audio, device=dev)
    model.train()
    for step in range(1, args.steps + 1):
        idx = torch.randint(0, len(PX), (args.batch,), device=dev)
        x, y = PX[idx].float(), PY[idx]
        x = x * torch.pow(10, torch.empty(len(x), 1, device=dev).uniform_(-6, 4) / 20)  # громкость
        mix = torch.rand(len(x), 1, device=dev) < 0.3                                    # ещё шум комнаты
        st = torch.randint(0, len(noise_gpu) - CLIP - 1, (len(x),), device=dev)
        nz = noise_gpu[st[:, None] + torch.arange(CLIP, device=dev)[None]]
        x = x + mix * nz * torch.empty(len(x), 1, device=dev).uniform_(0.1, 1.0)
        x = x.clamp(-1, 1)
        logits = model(logmel(x))[:, WARM - 2:]
        mx = logits.max(1).values
        loss = F.binary_cross_entropy_with_logits(mx, y)
        neg = y == 0
        if neg.any():  # у «не Джарвиса» давим и средний уровень, чтобы не было широких пиков
            loss = loss + 0.3 * F.softplus(torch.logsumexp(logits[neg], 1) - math.log(logits.shape[1])).mean()
        opt.zero_grad(); loss.backward(); opt.step(); sched.step()
        if step % 500 == 0 or step == 1:
            print(f"шаг {step}: loss {loss.item():.4f} [{time.time()-t0:.0f} с]", flush=True)

    # ------------------------------------------------------------ проверка как на колонке
    model.eval()
    def peak(seg):
        p = stream_probs(model, finalize(seg))
        return p[WARM:].max()
    vp = np.array([peak(s) for s in va_pos])
    vl = np.array([peak(s) for s in va_look])
    p_bg = stream_probs(model, bg_va)
    hours = len(bg_va) / SR / 3600
    thr_grid = np.linspace(0.05, 0.995, 190)
    print(f"\nпроверка: свои «Джарвис» {len(vp)}, похожие слова {len(vl)}, фон {hours*60:.1f} мин")
    table = []
    for thr in thr_grid:
        rec = (vp >= thr).mean() if len(vp) else 0
        fa_look = int((vl >= thr).sum())
        fa_bg = count_hits(p_bg, thr)
        table.append((thr, rec, fa_look, fa_bg))
    for thr, rec, fl, fb in table[::19]:
        print(f"  порог {thr:.2f}: узнаёт {rec*100:.0f}%, ложных на похожих {fl}/{len(vl)}, на фоне {fb} ({fb/hours:.1f} в час)")

    # пороги для ползунка чувствительности 1..10: от «осторожно» к «слышит всё»
    # 5 — самый низкий порог без ложных на отложенном фоне и не больше 1 ложного на похожих
    safe = [t for t, r, fl, fb in table if fb == 0 and fl <= max(1, len(vl) // 20)]
    t5 = min(safe) if safe else 0.9
    ts = [min(0.995, t5 + (0.995 - t5) * (5 - s) / 4) if s <= 5 else max(0.05, t5 - (t5 - 0.15) * (s - 5) / 5) for s in range(1, 11)]
    ts = [round(float(t), 4) for t in ts]
    if args.thresholds:
        ts = [float(v) for v in args.thresholds.split(",")]
    print("пороги чувствительности 1..10:", ts)
    r5 = (vp >= ts[4]).mean() * 100 if len(vp) else 0
    print(f"на чувствительности 5: узнаёт {r5:.0f}% своих «Джарвис» (отложенные блоки)")

    export(model, ts, os.path.join(args.out, "wake.bin"))
    torch.save(model.state_dict(), os.path.join(args.out, "model.pt"))
    # тестовый вектор для проверки kws.go: кусок своей записи и ожидаемые вероятности
    test = finalize(va_pos[0]) if va_pos else noise.take(CLIP)
    p = torch.sigmoid(model(logmel(torch.tensor(test[None], device=dev))))[0].detach().cpu().numpy()
    json.dump({"pcm": (test * 32768).clip(-32768, 32767).astype(int).tolist(), "probs": p.tolist()},
              open(os.path.join(args.out, "testvec.json"), "w"))
    json.dump({"thresholds": ts, "val_pos": len(vp), "recall_s5": r5, "bg_minutes": hours * 60,
               "table": [[round(a, 4), round(float(b), 4), c, d] for a, b, c, d in table]},
              open(os.path.join(args.out, "report.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(f"готово за {time.time()-t0:.0f} с → {args.out}/wake.bin")


def export(model, thresholds, path):
    hdr = {"sample_rate": SR, "win": WIN, "hop": HOP, "n_fft": NFFT, "n_mels": NMELS, "fmin": FMIN, "fmax": FMAX,
           "channels": CH, "dilations": DIL, "kernel": K, "thresholds": thresholds}
    parts = [model.mean, model.std, model.proj.weight, model.proj.bias]
    for b in model.blocks:
        parts += [b.weight, b.bias]
    parts += [model.head.weight, model.head.bias]
    blob = b"".join(p.detach().float().cpu().contiguous().numpy().astype("<f4").tobytes() for p in parts)
    h = json.dumps(hdr).encode()
    with open(path, "wb") as f:
        f.write(b"JKWS1" + struct.pack("<I", len(h)) + h + blob)
    print(f"модель: {path} ({(len(blob) + len(h)) // 1024} КБ, параметров {len(blob) // 4})")


if __name__ == "__main__":
    main()
