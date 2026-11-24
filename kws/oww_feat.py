"""Признаки openWakeWord (мел-спектр + speech embedding Google, Apache 2.0) — точная копия того, что считает
jarvisd/oww.go на колонке. Веса берутся из melspectrogram.onnx и embedding_model.onnx.

Звук: 16 кГц, значения в масштабе int16 (как у openWakeWord). Поток режется шагами по 1280 отсчётов (80 мс):
на каждом шаге из последних 1760 отсчётов — 8 кадров мел-спектра, потолок 80 дБ считается по этим 8 кадрам.
Признак (эмбеддинг 96) — на каждое окно из 76 кадров с шагом 8; окна начинаются с кадра 4 (как в потоке).
"""
import struct
import os

import numpy as np
import onnx
import torch
import torch.nn.functional as F
from onnx import numpy_helper

OWW = os.path.join(os.environ.get("OWW_MODELS", os.path.join(os.path.dirname(__file__), "onnx")), "")
STEP, CTX = 1280, 480


def _load():
    mel = onnx.load(OWW + "melspectrogram.onnx")
    init = {t.name: numpy_helper.to_array(t) for t in mel.graph.initializer}
    convs = [init[n.input[1]] for n in mel.graph.node if n.op_type == "Conv"]
    real, imag = convs[0][:, 0, :], convs[1][:, 0, :]
    melmat = [init[n.input[1]] for n in mel.graph.node if n.op_type == "MatMul"][0]
    clip = [n for n in mel.graph.node if n.op_type == "Clip"][0]
    floor = float(init[clip.input[1]]) if clip.input[1] in init else 1e-10
    emb = onnx.load(OWW + "embedding_model.onnx")
    einit = {t.name: numpy_helper.to_array(t) for t in emb.graph.initializer}
    layers = []
    for n in emb.graph.node:
        a = {x.name: onnx.helper.get_attribute_value(x) for x in n.attribute}
        if n.op_type == "Conv":
            w = einit[n.input[1]]
            b = einit[n.input[2]] if len(n.input) > 2 else np.zeros(w.shape[0], np.float32)
            layers.append(("conv", w.astype(np.float32), b.astype(np.float32)))
        elif n.op_type == "MaxPool":
            layers.append(("pool", tuple(a["kernel_shape"]), None))
        elif n.op_type == "LeakyRelu":
            layers.append(("act", None, None))  # LeakyReLU 0.2, затем max(x, -0.4)
    return real.astype(np.float32), imag.astype(np.float32), melmat.astype(np.float32), floor, layers


REAL, IMAG, MELMAT, FLOOR, LAYERS = _load()


def mel_frames(x):
    """x: float32 в масштабе int16 → [кадры, 32] (уже /10 + 2, как у openWakeWord)."""
    out = []
    for end in range(STEP, len(x) + 1, STEP):
        a = max(0, end - STEP - CTX)
        chunk = x[a:end]
        if len(chunk) < STEP + CTX:
            chunk = np.pad(chunk, (STEP + CTX - len(chunk), 0))
        n = (len(chunk) - 512) // 160 + 1
        idx = np.arange(512)[None, :] + 160 * np.arange(n)[:, None]
        fr = chunk[idx]                                   # [n, 512]
        p = (fr @ REAL.T) ** 2 + (fr @ IMAG.T) ** 2       # [n, 257]
        m = p @ MELMAT                                    # [n, 32]
        db = 10 * np.log10(np.maximum(m, FLOOR))
        db = np.maximum(db, db.max() - 80)
        out.append(db / 10 + 2)
    return np.concatenate(out, 0).astype(np.float32) if out else np.zeros((0, 32), np.float32)


class EmbedNet(torch.nn.Module):
    """Сеть speech embedding в PyTorch с весами из ONNX."""

    def __init__(self):
        super().__init__()
        self.ops = []
        ws = []
        for kind, w, b in LAYERS:
            if kind == "conv":
                ws.append(torch.from_numpy(w))
                ws.append(torch.from_numpy(b))
                self.ops.append(("conv", len(ws) - 2, w.shape[3]))
            elif kind == "act":
                self.ops.append(("act", 0, 0))
            else:
                self.ops.append(("pool", w, 0))
        self.ws = ws

    @torch.no_grad()
    def forward(self, x):
        h = x[:, None]
        for kind, a, b in self.ops:
            if kind == "conv":
                if b == 3:
                    h = F.pad(h, (1, 1, 0, 0))
                h = F.conv2d(h, self.ws[a], self.ws[a + 1])
            elif kind == "act":
                h = torch.clamp_min(F.leaky_relu(h, 0.2), -0.4)
            else:
                h = F.max_pool2d(h, a)
        return h.reshape(h.shape[0], -1)


NET = EmbedNet()


def embeddings(x, device="cpu"):
    """x: float32 в масштабе int16 → [окна, 96]: окна по 76 кадров с шагом 8, начиная с кадра 4."""
    m = mel_frames(x)
    starts = list(range(4, len(m) - 76 + 1, 8))
    if not starts:
        return np.zeros((0, 96), np.float32)
    win = np.stack([m[s:s + 76] for s in starts])
    out = []
    for i in range(0, len(win), 512):
        out.append(NET(torch.from_numpy(win[i:i + 512])).numpy())
    return np.concatenate(out, 0)


def export_go(path):
    """Веса для jarvisd/oww.go: окно, мел-матрица, свёртки по порядку."""
    with open(path, "wb") as f:
        f.write(b"OWW1")
        window = REAL[0]  # нулевая частота: cos(0) = 1, остаётся само окно
        f.write(struct.pack("<f", FLOOR))
        f.write(window.astype("<f4").tobytes())
        f.write(MELMAT.astype("<f4").tobytes())                 # [257, 32]
        f.write(struct.pack("<I", sum(1 for k, _, _ in LAYERS if k == "conv")))
        for kind, w, b in LAYERS:
            if kind == "conv":
                f.write(struct.pack("<4I", *w.shape))
                f.write(w.astype("<f4").tobytes())
                f.write(b.astype("<f4").tobytes())
    # порядок слоёв в Go задан кодом; здесь проверяем, что он такой же
    return [(k, (w.shape if k == "conv" else w)) for k, w, _ in LAYERS]


if __name__ == "__main__":
    # проверка: наш расчёт против самих ONNX-моделей
    import onnxruntime as ort
    rng = np.random.default_rng(0)
    x = (rng.standard_normal(16000 * 3) * 3000).astype(np.float32)
    k = np.arange(512)
    w = REAL[0]
    ok = np.allclose(REAL[5], w * np.cos(2 * np.pi * 5 * k / 512), atol=1e-4) and np.allclose(IMAG[5], -w * np.sin(2 * np.pi * 5 * k / 512), atol=1e-4)
    print("ядра STFT = окно × cos/−sin:", ok, " floor", FLOOR)
    ms = ort.InferenceSession(OWW + "melspectrogram.onnx")
    ref = ms.run(None, {"input": x[None, :1760]})[0][:, 0, :, :].reshape(-1, 32) / 10 + 2
    mine = mel_frames(x[:1760])
    print("мел:", ref.shape, mine.shape, "макс. расхождение", float(np.abs(ref - mine[: len(ref)]).max()))
    es = ort.InferenceSession(OWW + "embedding_model.onnx")
    m = mel_frames(x)
    win = m[4:80][None, :, :, None].astype(np.float32)
    r = es.run(None, {"input_1": win})[0].reshape(-1)
    me = NET(torch.from_numpy(m[4:80][None])).numpy().reshape(-1)
    print("эмбеддинг: макс. расхождение", float(np.abs(r - me).max()), "норма", float(np.abs(r).max()))
    print(export_go(OWW + "oww.bin"))
