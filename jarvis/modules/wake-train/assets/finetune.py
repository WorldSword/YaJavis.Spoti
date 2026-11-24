"""Дообучение слова «Джарвис» на записях с колонки — поверх модели, которая уже стоит (wake.bin).

    pip install torch numpy
    python finetune.py                 # из распакованной папки архива

Что делает:
  1. берёт wake.bin из архива — модель, которая сейчас на колонке;
  2. дообучает её на pos/ (настоящие «Джарвис»), false/ (ложные срабатывания) и bg/ (фон комнаты).
     Веса держатся рядом с исходными (L2-SP), поэтому модель не «забывает» то, чему её учили раньше;
  3. сравнивает «до» и «после» на отложенных записях и сохраняет wake_new.bin.
Загрузите wake_new.bin в настройках: «Обучение» → «Загрузить модель». Прежняя модель сохранится,
её можно вернуть одной кнопкой. Видеокарта не обязательна: на процессоре — пара минут.

Полезные ключи: --steps 1200 (дольше учить), --sens 7 (какую чувствительность вы ставите на колонке),
--all (учить на всех записях, без отложенных для проверки), --near-pos (считать near/*_r.wav «Джарвисом»).
"""
import argparse, glob, json, math, os, random, struct, sys, time, wave

try:
    import numpy as np
    import torch
    import torch.nn as nn
    import torch.nn.functional as F
except ImportError:
    sys.exit("нужны numpy и torch:  pip install torch numpy")

HERE = os.path.dirname(os.path.abspath(__file__))
CLIP_S, WARM = 3.5, 100  # длина обучающего клипа; первые 1.0 с (100 кадров) — прогрев, их не оцениваем


# ------------------------------------------------------------------ модель: формат wake.bin (как в jarvisd/kws.go)
def load_bin(path):
    b = open(path, "rb").read()
    if b[:5] != b"JKWS1":
        sys.exit(f"{path}: это не модель слова (нет JKWS1)")
    n = struct.unpack("<I", b[5:9])[0]
    return json.loads(b[9:9 + n]), np.frombuffer(b[9 + n:], "<f4").copy()


def save_bin(path, hdr, model):
    parts = [model.mean, model.std, model.proj.weight, model.proj.bias]
    for blk in model.blocks:
        parts += [blk.weight, blk.bias]
    parts += [model.head.weight, model.head.bias]
    blob = b"".join(p.detach().float().cpu().contiguous().numpy().astype("<f4").tobytes() for p in parts)
    h = json.dumps(hdr).encode()
    with open(path, "wb") as f:
        f.write(b"JKWS1" + struct.pack("<I", len(h)) + h + blob)


class KWS(nn.Module):
    def __init__(self, hdr):
        super().__init__()
        nm, ch, self.k, self.dil = hdr["n_mels"], hdr["channels"], hdr["kernel"], hdr["dilations"]
        self.register_buffer("mean", torch.zeros(nm))
        self.register_buffer("std", torch.ones(nm))
        self.proj = nn.Conv1d(nm, ch, 1)
        self.blocks = nn.ModuleList(nn.Conv1d(ch, ch, self.k, dilation=d) for d in self.dil)
        self.head = nn.Conv1d(ch, 1, 1)
        self.drop = nn.Dropout(0.1)

    def forward(self, feat):
        x = (feat - self.mean[:, None]) / self.std[:, None]
        x = F.relu(self.proj(x))
        for blk, d in zip(self.blocks, self.dil):
            x = x + self.drop(F.relu(blk(F.pad(x, ((self.k - 1) * d, 0)))))
        return self.head(x)[:, 0]

    def load_flat(self, w):
        i = 0
        for t in [self.mean, self.std, self.proj.weight, self.proj.bias] + \
                 [p for blk in self.blocks for p in (blk.weight, blk.bias)] + [self.head.weight, self.head.bias]:
            n = t.numel()
            if i + n > len(w):
                sys.exit("wake.bin обрезан или от другой сети")
            t.data.copy_(torch.from_numpy(w[i:i + n]).view_as(t))
            i += n
        if i != len(w):
            sys.exit("wake.bin не совпадает с сетью из заголовка")


# ------------------------------------------------------------------ признаки (лог-мел, как на колонке)
def mel_bank(h):
    sr, nfft, nm = h["sample_rate"], h["n_fft"], h["n_mels"]
    hz2mel = lambda f: 2595 * np.log10(1 + f / 700)
    mel2hz = lambda m: 700 * (10 ** (m / 2595) - 1)
    bins = nfft // 2 + 1
    pts = mel2hz(np.linspace(hz2mel(h["fmin"]), hz2mel(h["fmax"]), nm + 2))
    f = np.arange(bins) * sr / nfft
    bank = np.zeros((nm, bins), np.float32)
    for m in range(nm):
        l, c, r = pts[m], pts[m + 1], pts[m + 2]
        up, down = (f > l) & (f < c), (f >= c) & (f < r)
        bank[m, up] = (f[up] - l) / (c - l)
        bank[m, down] = (r - f[down]) / (r - c)
    return bank


class Feat:
    def __init__(self, h, dev):
        self.h = h
        self.bank = torch.tensor(mel_bank(h), device=dev)
        self.window = torch.hann_window(h["win"], periodic=True, dtype=torch.float64, device=dev).float()

    def __call__(self, x):  # [B, N] → [B, n_mels, T]
        fr = x.unfold(1, self.h["win"], self.h["hop"]) * self.window
        spec = torch.fft.rfft(fr, n=self.h["n_fft"])
        return torch.log((spec.real ** 2 + spec.imag ** 2) @ self.bank.T + 1e-6).transpose(1, 2)


# ------------------------------------------------------------------ данные
def read_wav(p, sr):
    try:
        with wave.open(p) as w:
            if w.getsampwidth() != 2 or w.getframerate() != sr:
                return None
            x = np.frombuffer(w.readframes(w.getnframes()), "<i2").astype(np.float32) / 32768
            if w.getnchannels() > 1:
                x = x.reshape(-1, w.getnchannels()).mean(1)
            return x
    except (wave.Error, EOFError):
        return None


def load_dir(root, name, sr, only_reviewed=False):
    out = []
    for p in sorted(glob.glob(os.path.join(root, name, "*.wav"))):
        if only_reviewed and "_r" not in os.path.basename(p):
            continue
        x = read_wav(p, sr)
        if x is not None and len(x) > sr // 2:
            out.append(x)
    return out


def rms(x):
    return float(np.sqrt((x ** 2).mean() + 1e-12))


class Noise:
    def __init__(self, bgs, sr):
        self.audio = np.concatenate(bgs) if bgs else np.random.randn(sr * 20).astype(np.float32) * 0.002

    def take(self, n):
        if len(self.audio) <= n:
            return np.resize(self.audio, n)
        i = random.randint(0, len(self.audio) - n - 1)
        return self.audio[i:i + n].copy()


def stretch(x):
    r = random.uniform(0.9, 1.1)  # темп ±10% — линейной интерполяцией, без scipy
    idx = np.arange(0, len(x) - 1, r)
    return np.interp(idx, np.arange(len(x)), x).astype(np.float32)


def at_end(x, clip, noise):
    """Запись с колонки (слово в конце) → клип нужной длины, спереди — фон."""
    x = x[-clip:]
    return np.concatenate([noise.take(clip - len(x)) * 0.5, x]) if len(x) < clip else x


def augment(x, clip, noise, word):
    if word and random.random() < 0.35:
        x = stretch(x)
    x = at_end(x, clip, noise)
    if random.random() < 0.6:
        n = noise.take(clip)
        x = x + n * (rms(x) / (rms(n) + 1e-9)) * 10 ** (-random.uniform(8, 30) / 20)
    if not word and random.random() < 0.5:  # у не-слова можно и сдвинуть
        x = np.roll(x, random.randint(-clip // 6, clip // 6))
    x = x * 10 ** (random.uniform(-8, 6) / 20)
    peak = np.abs(x).max()
    return (x / peak * 0.95 if peak > 0.99 else x).astype(np.float32)


# ------------------------------------------------------------------ проверка «как на колонке»
@torch.no_grad()
def probs(model, feat, x, dev):
    model.eval()
    p = torch.sigmoid(model(feat(torch.tensor(x[None], device=dev))))[0].cpu().numpy()
    return np.convolve(p, np.ones(3) / 3)[:len(p)]  # колонка усредняет 3 последних кадра


def peak(model, feat, x, clip, noise, dev):
    return float(probs(model, feat, at_end(x, clip, noise), dev)[WARM:].max())


def hits(p, thr, cooldown=150):
    n, i = 0, 0
    while i < len(p):
        if p[i] >= thr:
            n, i = n + 1, i + cooldown
        else:
            i += 1
    return n


def report(tag, model, feat, val, clip, noise, thr, dev, bg_val):
    pos = [peak(model, feat, x, clip, noise, dev) for x in val["pos"]]
    neg = [peak(model, feat, x, clip, noise, dev) for x in val["false"]]
    rec = sum(p >= thr for p in pos)
    fa = sum(p >= thr for p in neg)
    avg = lambda v: f"{sum(v) / len(v):.2f}" if v else "—"
    line = (f"  {tag}: узнаёт {rec}/{len(pos)} «Джарвис» (уверенность в среднем {avg(pos)}), "
            f"срабатывает зря на {fa}/{len(neg)} ложных ({avg(neg)})")
    if len(bg_val) > clip:
        fb = hits(probs(model, feat, bg_val, dev), thr)
        line += f", на фоне {fb} раз за {len(bg_val) / 16000 / 60:.1f} мин"
    print(line)
    return rec, fa


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dir", default=HERE, help="папка распакованного архива (по умолчанию — рядом со скриптом)")
    ap.add_argument("--model", default="", help="исходная модель (по умолчанию wake.bin из архива)")
    ap.add_argument("--out", default="", help="куда сохранить (по умолчанию wake_new.bin рядом)")
    ap.add_argument("--steps", type=int, default=800)
    ap.add_argument("--batch", type=int, default=32)
    ap.add_argument("--lr", type=float, default=3e-4)
    ap.add_argument("--keep", type=float, default=2e-3, help="насколько держаться исходных весов (L2-SP)")
    ap.add_argument("--sens", type=int, default=0, help="чувствительность 1..10 для проверки (по умолчанию — как на колонке)")
    ap.add_argument("--all", action="store_true", help="учить на всех записях, без отложенных для проверки")
    ap.add_argument("--near-pos", action="store_true", help="near/*_r.wav (проверенные вами) — тоже «Джарвис»")
    ap.add_argument("--seed", type=int, default=1)
    a = ap.parse_args()
    random.seed(a.seed); np.random.seed(a.seed); torch.manual_seed(a.seed)
    dev = torch.device("cuda" if torch.cuda.is_available() else "cpu")

    src = a.model or os.path.join(a.dir, "wake.bin")
    if not os.path.exists(src):
        sys.exit(f"нет {src}: положите рядом модель с колонки или укажите --model")
    hdr, w = load_bin(src)
    sr = hdr["sample_rate"]
    clip = int(CLIP_S * sr)
    if not a.sens:  # как стоит на колонке (wake.json в архиве), иначе 5
        try:
            a.sens = int(json.load(open(os.path.join(a.dir, "wake.json"), encoding="utf-8")).get("sensitivity") or 5)
        except (OSError, ValueError):
            a.sens = 5
    thr = hdr["thresholds"][max(1, min(10, a.sens)) - 1]

    pos = load_dir(a.dir, "pos", sr)
    if a.near_pos:
        pos += load_dir(a.dir, "near", sr, only_reviewed=True)
    false = load_dir(a.dir, "false", sr)
    bgs = load_dir(a.dir, "bg", sr)
    print(f"записи: «Джарвис» {len(pos)}, ложные {len(false)}, фон {len(bgs)} ({sum(map(len, bgs)) / sr / 60:.1f} мин); "
          f"считаю на {'видеокарте' if dev.type == 'cuda' else 'процессоре'}")
    if len(pos) < 5 and len(false) < 5:
        sys.exit("записей слишком мало — включите на колонке режим обучения и поговорите с Джарвисом пару дней")

    # отложенные для честной проверки: каждая 6-я запись
    val = {"pos": [], "false": []}
    tr = {"pos": pos, "false": false}
    if not a.all:
        for k in tr:
            if len(tr[k]) >= 6:
                val[k] = tr[k][::6]
                tr[k] = [x for i, x in enumerate(tr[k]) if i % 6]
    bg_all = np.concatenate(bgs) if bgs else np.zeros(0, np.float32)
    cut = int(len(bg_all) * 0.85) if not a.all else len(bg_all)
    noise = Noise([bg_all[:cut]] if cut > sr else [], sr)
    bg_val = bg_all[cut:]

    model = KWS(hdr).to(dev)
    model.load_flat(w)
    feat = Feat(hdr, dev)
    ref = [p.detach().clone() for p in model.parameters()]

    has_val = val["pos"] or val["false"]
    if has_val:
        print(f"проверка на отложенных (порог чувствительности {a.sens}: {thr}):")
        b_rec, b_fa = report("до", model, feat, val, clip, noise, thr, dev, bg_val)

    def batch(n):
        xs, ys = [], []
        for _ in range(n):
            r = random.random()
            if r < 0.4 and tr["pos"]:
                xs.append(augment(random.choice(tr["pos"]), clip, noise, True)); ys.append(1)
            elif r < 0.75 and tr["false"]:
                xs.append(augment(random.choice(tr["false"]), clip, noise, False)); ys.append(0)
            else:
                x = noise.take(clip) * 10 ** (random.uniform(-6, 6) / 20)
                xs.append(x.astype(np.float32)); ys.append(0)
        return torch.tensor(np.stack(xs), device=dev), torch.tensor(ys, device=dev, dtype=torch.float32)

    opt = torch.optim.AdamW(model.parameters(), lr=a.lr, weight_decay=0)
    sched = torch.optim.lr_scheduler.CosineAnnealingLR(opt, a.steps)
    t0 = time.time()
    for step in range(1, a.steps + 1):
        model.train()
        x, y = batch(a.batch)
        logits = model(feat(x))[:, WARM - 2:]
        loss = F.binary_cross_entropy_with_logits(logits.max(1).values, y)
        neg = y == 0
        if neg.any():  # у «не Джарвиса» давим и широкие пики
            loss = loss + 0.3 * F.softplus(torch.logsumexp(logits[neg], 1) - math.log(logits.shape[1])).mean()
        loss = loss + a.keep * sum(((p - r) ** 2).sum() for p, r in zip(model.parameters(), ref))
        opt.zero_grad(); loss.backward(); opt.step(); sched.step()
        if step == 1 or step % 100 == 0:
            done = step / a.steps
            left = (time.time() - t0) / done * (1 - done)
            print(f"  шаг {step}/{a.steps}: loss {loss.item():.4f}, осталось ~{left:.0f} с", flush=True)

    if has_val:
        a_rec, a_fa = report("после", model, feat, val, clip, noise, thr, dev, bg_val)
        if a_rec < b_rec or a_fa > b_fa:
            print("  ⚠ на отложенных стало хуже — попробуйте --steps 400 или --keep 5e-3; модель всё равно сохраню")
    out = a.out or os.path.join(a.dir, "wake_new.bin")
    save_bin(out, hdr, model)
    print(f"готово: {out} — загрузите в настройках: «Обучение» → «Загрузить модель»")


if __name__ == "__main__":
    main()
