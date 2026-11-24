"""Новое слово «Джарвис» на признаках openWakeWord: готовая сеть speech embedding (Google, обучена на тысячах
часов речи) превращает звук в «отпечатки» по 96 чисел каждые 80 мс, а мы учим только маленькую сеть сверху:
16 отпечатков подряд (1,28 с) → «это Джарвис?».

  python -I -X utf8 train_oww.py --rec <записи> --synth data/synth --hard hard --pos pos_live --out out_oww
"""
import argparse, glob, json, math, os, random, struct, sys, time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F

import oww_feat
from train import (SR, CLIP, Noise, augment_real, augment_synth, count_hits, finalize, load_recordings, load_synth,
                   nonzero_runs, read_wav, rms)

dev = torch.device("cuda" if torch.cuda.is_available() else "cpu")
SEQ = 16          # отпечатков на входе головы
EMB = 96


class Head(nn.Module):
    def __init__(self, hidden=96, drop=0.0):
        super().__init__()
        self.drop = nn.Dropout(drop)
        self.l1 = nn.Linear(SEQ * EMB, hidden)
        self.l2 = nn.Linear(hidden, hidden)
        self.l3 = nn.Linear(hidden, 1)

    def forward(self, e):  # e: [B, P, SEQ*EMB] → логиты [B, P]
        h = self.drop(F.relu(self.l1(self.drop(e))))
        h = self.drop(F.relu(self.l2(h)))
        return self.l3(h)[..., 0]


def windows(emb):
    """[N, 96] → [N-SEQ+1, SEQ*96]: вход головы на каждом шаге 80 мс."""
    n = len(emb) - SEQ + 1
    if n <= 0:
        return np.zeros((0, SEQ * EMB), np.float32)
    idx = np.arange(SEQ)[None, :] + np.arange(n)[:, None]
    return emb[idx].reshape(n, SEQ * EMB)


def embed_clips(clips):
    """Клипы (float, -1..1) → отпечатки; сеть считается на видеокарте пачками."""
    oww_feat.NET.ws = [w.to(dev) for w in oww_feat.NET.ws]
    out, wins, owner = [], [], []
    mels = []
    for x in clips:
        m = oww_feat.mel_frames(np.asarray(x, np.float32) * 32768)
        mels.append(m)
    for ci, m in enumerate(mels):
        for s in range(4, len(m) - 76 + 1, 8):
            wins.append(m[s:s + 76]); owner.append(ci)
    res = []
    W = np.stack(wins)
    for i in range(0, len(W), 4096):
        res.append(oww_feat.NET(torch.from_numpy(W[i:i + 4096]).to(dev)).cpu().numpy())
    E = np.concatenate(res)
    owner = np.array(owner)
    return [E[owner == ci] for ci in range(len(clips))]


def embed_long(x, part=SR * 60):
    """Длинная запись (фон) → отпечатки подряд, кусками по минуте с перекрытием."""
    E = embed_clips([x])[0]
    return E


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rec", required=True)
    ap.add_argument("--synth", required=True)
    ap.add_argument("--hard", default="")
    ap.add_argument("--pos", default="")
    ap.add_argument("--out", default="out_oww")
    ap.add_argument("--pool", type=int, default=24000)
    ap.add_argument("--steps", type=int, default=8000)
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--drop", type=float, default=0.3)
    ap.add_argument("--wd", type=float, default=0.05)
    ap.add_argument("--negw", type=float, default=1.0, help="вес «не Джарвиса» в потере")
    args = ap.parse_args()
    random.seed(1); np.random.seed(1); torch.manual_seed(1)
    os.makedirs(args.out, exist_ok=True)
    t0 = time.time()

    pos_real, neg_look, bgs = load_recordings(args.rec)
    syn_pos, syn_neg = load_synth(args.synth)
    VAL_POS_BLOCKS, VAL_LOOK_BLOCK = ({2, 7}, 3) if not args.all else (set(), -1)
    tr_pos = [s for s, b in pos_real if b not in VAL_POS_BLOCKS]
    va_pos = [s for s, b in pos_real if b in VAL_POS_BLOCKS]
    tr_look = [s for s, b in neg_look if b != VAL_LOOK_BLOCK]
    va_look = [s for s, b in neg_look if b == VAL_LOOK_BLOCK]
    bg_all = np.concatenate([np.concatenate(nonzero_runs(b, SR)) for b in bgs])
    cut = int(len(bg_all) * (0.75 if not args.all else 0.9))
    bg_tr, bg_va = bg_all[:cut], bg_all[cut:]
    noise = Noise([bg_tr])
    hard = [h for h in (read_wav(p) for p in glob.glob(os.path.join(args.hard, "*.wav"))) if len(h) > SR] if args.hard else []
    live = [h for h in (read_wav(p) for p in glob.glob(os.path.join(args.pos, "*.wav"))) if len(h) > SR] if args.pos else []
    print(f"данные: свои {len(tr_pos)}+{len(va_pos)}, похожие {len(tr_look)}+{len(va_look)}, синтетика {len(syn_pos)}/{len(syn_neg)}, "
          f"живые {len(live)}, ложные {len(hard)}, фон {len(bg_tr)/SR/60:.1f}+{len(bg_va)/SR/60:.1f} мин", flush=True)

    def make(n):
        xs, ys = [], []
        for _ in range(n):
            r = random.random()
            if r < 0.30:
                x, y = augment_real(random.choice(tr_pos), noise), 1
            elif r < 0.50:
                x, y = augment_synth(random.choice(syn_pos), noise), 1
            elif r < 0.53 and live:
                h = random.choice(live)
                x = np.concatenate([noise.take(max(0, CLIP - len(h))) * 0.5, h])[-CLIP:] * 10 ** (random.uniform(-8, 6) / 20)
                y = 1
            elif r < 0.60 and hard:
                h = random.choice(hard)
                x = np.concatenate([noise.take(max(0, CLIP - len(h))) * 0.5, h])[-CLIP:]
                x, y = augment_real(x, noise), 0
            elif r < 0.66:
                x, y = augment_real(random.choice(tr_look), noise), 0
            elif r < 0.78:
                x, y = augment_synth(random.choice(syn_neg), noise), 0
            else:
                i = random.randint(0, len(bg_tr) - CLIP - 1)
                x, y = bg_tr[i:i + CLIP] * 10 ** (random.uniform(-6, 6) / 20), 0
            if random.random() < 0.3:  # ещё шум комнаты
                x = x + noise.take(len(x)) * random.uniform(0.1, 1.0)
            xs.append(np.clip(finalize(x), -1, 1)); ys.append(y)
        return xs, ys

    cache = os.path.join(args.out, f"pool_{args.pool}_{int(args.all)}.npz")
    if os.path.exists(cache):
        z = np.load(cache)
        E, Yn = z["E"], z["Y"]
    else:
        Es, Yn = [], []
        for i in range(0, args.pool, 2000):
            xs, ys = make(min(2000, args.pool - i))
            Es += embed_clips(xs)
            Yn += ys
            print(f"  отпечатки: {i + len(xs)}/{args.pool} [{time.time()-t0:.0f} с]", flush=True)
        L = min(len(e) for e in Es)
        E, Yn = np.stack([e[-L:] for e in Es]).astype(np.float16), np.array(Yn, np.float32)
        np.savez(cache, E=E, Y=Yn)
    Et = torch.tensor(E, device=dev).float()                            # [N, L, 96]
    X = Et.unfold(1, SEQ, 1).permute(0, 1, 3, 2).reshape(len(Et), -1, SEQ * EMB)  # [N, P, 1536]
    Y = torch.tensor(Yn, dtype=torch.float32, device=dev)
    print(f"пул: {len(X)} клипов по {X.shape[1]} шагов, «Джарвис» {int(Y.sum())} [{time.time()-t0:.0f} с]", flush=True)

    head = Head(drop=args.drop).to(dev)
    opt = torch.optim.AdamW(head.parameters(), lr=1e-3, weight_decay=args.wd)
    sched = torch.optim.lr_scheduler.OneCycleLR(opt, max_lr=2e-3, total_steps=args.steps, pct_start=0.1)
    for step in range(1, args.steps + 1):
        idx = torch.randint(0, len(X), (256,), device=dev)
        x, y = X[idx], Y[idx]
        x = x + torch.randn_like(x) * 0.05 * x.std()
        lg = head(x)
        mx = lg.max(1).values
        loss = F.binary_cross_entropy_with_logits(mx, y, weight=torch.where(y == 0, args.negw, 1.0))
        neg = y == 0
        if neg.any():
            loss = loss + 0.3 * F.softplus(torch.logsumexp(lg[neg], 1) - math.log(lg.shape[1])).mean()
        opt.zero_grad(); loss.backward(); opt.step(); sched.step()
        if step % 1000 == 0 or step == 1:
            print(f"шаг {step}: loss {loss.item():.4f} [{time.time()-t0:.0f} с]", flush=True)

    # ------------------------------------------------------------ проверка как на колонке (шаг 80 мс)
    head.eval()

    @torch.no_grad()
    def probs(e):
        w = windows(e)
        if not len(w):
            return np.zeros(0)
        return torch.sigmoid(head(torch.tensor(w, device=dev)[None]))[0].cpu().numpy()

    vp = np.array([probs(e).max(initial=0) for e in embed_clips([finalize(s) for s in va_pos])]) if va_pos else np.zeros(0)
    vl = np.array([probs(e).max(initial=0) for e in embed_clips([finalize(s) for s in va_look])]) if va_look else np.zeros(0)
    p_bg = probs(embed_long(bg_va))
    hours = len(bg_va) / SR / 3600
    table = []
    for thr in list(np.linspace(0.05, 0.995, 190)) + [0.997, 0.998, 0.999]:
        rec = (vp >= thr).mean() if len(vp) else 0
        table.append((thr, rec, int((vl >= thr).sum()), count_hits(p_bg, thr, cooldown=19)))
    print(f"\nпроверка: свои «Джарвис» {len(vp)}, похожие {len(vl)}, фон {hours*60:.1f} мин")
    for thr, rec, fl, fb in table[::19] + table[-6:]:
        print(f"  порог {thr:.2f}: узнаёт {rec*100:.0f}%, ложных на похожих {fl}/{len(vl)}, на фоне {fb} ({fb/hours:.1f} в час)")
    safe = [t for t, r, fl, fb in table if fb == 0 and fl <= max(1, len(vl) // 20)]
    t5 = min(safe) if safe else 0.9
    ts = [min(0.995, t5 + (0.995 - t5) * (5 - s) / 4) if s <= 5 else max(0.05, t5 - (t5 - 0.15) * (s - 5) / 5) for s in range(1, 11)]
    ts = [round(float(t), 4) for t in ts]
    r5 = (vp >= ts[4]).mean() * 100 if len(vp) else 0
    print("пороги 1..10:", ts)
    print(f"на чувствительности 5: узнаёт {r5:.0f}% своих «Джарвис», на 7: {(vp >= ts[6]).mean()*100 if len(vp) else 0:.0f}%")

    # ------------------------------------------------------------ веса для jarvisd/oww.go
    path = os.path.join(args.out, "wake_oww.bin")
    with open(path, "wb") as f:
        f.write(b"OWWH")
        f.write(struct.pack("<3I", SEQ, EMB, head.l1.out_features))
        f.write(struct.pack("<10f", *ts))
        for l in (head.l1, head.l2, head.l3):
            f.write(l.weight.detach().cpu().numpy().astype("<f4").tobytes())
            f.write(l.bias.detach().cpu().numpy().astype("<f4").tobytes())
    # тестовый вектор для Go: звук и ожидаемые вероятности
    test = finalize(va_pos[0]) if va_pos else noise.take(CLIP)
    e = embed_clips([test])[0]
    json.dump({"pcm": (np.asarray(test) * 32768).clip(-32768, 32767).astype(int).tolist(), "emb0": e[0].tolist(),
               "probs": probs(e).tolist()}, open(os.path.join(args.out, "testvec.json"), "w"))
    json.dump({"thresholds": ts, "recall_s5": r5, "table": [[round(a, 4), round(float(b), 4), c, d] for a, b, c, d in table]},
              open(os.path.join(args.out, "report.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(f"готово за {time.time()-t0:.0f} с → {path} ({os.path.getsize(path)//1024} КБ)")


if __name__ == "__main__":
    main()
