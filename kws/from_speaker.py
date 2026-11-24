"""Дообучение слова «Джарвис» по архиву с колонки (настройки → «Обучение» → «Скачать архив»).

    python kws/from_speaker.py jarvis-train-XXXX.zip [--rec папка_записей] [--no-train]

Раскладывает записи: pos/ → pos_live/ (живые «Джарвис»), false/ → hard/ (ложные срабатывания),
bg/ → bg_live/ (фон комнаты). near/ («почти оклики») не используются — сначала разметьте их в настройках:
настоящие «Джарвис» переложите в «Джарвис», остальные — в «Не Джарвис».
Потом запускает train.py (нужна видеокарта NVIDIA и PyTorch с CUDA) → out_next/wake.bin.
Готовый файл загрузите в настройках: «Обучение» → «Загрузить wake.bin».
"""
import argparse, os, subprocess, sys, zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
FOLDERS = {"pos": "pos_live", "false": "hard", "bg": "bg_live"}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("zip", help="архив с колонки")
    ap.add_argument("--rec", default=os.environ.get("JARVIS_REC", os.path.join(HERE, "recordings")),
                    help="ваши исходные записи «Джарвис» (из записывалки), по ним модель училась изначально")
    ap.add_argument("--synth", default=os.path.join(HERE, "data", "synth"))
    ap.add_argument("--out", default=os.path.join(HERE, "out_next"))
    ap.add_argument("--no-train", action="store_true", help="только разложить записи")
    args = ap.parse_args()

    counts = {}
    with zipfile.ZipFile(args.zip) as z:
        for info in z.infolist():
            parts = info.filename.replace("\\", "/").split("/")
            if len(parts) != 2 or not parts[1].endswith(".wav") or parts[0] not in FOLDERS:
                if len(parts) == 2 and parts[0] == "near":
                    counts["near (не используются)"] = counts.get("near (не используются)", 0) + 1
                continue
            name = os.path.basename(parts[1])  # без путей из архива
            dst_dir = os.path.join(HERE, FOLDERS[parts[0]])
            os.makedirs(dst_dir, exist_ok=True)
            with z.open(info) as src, open(os.path.join(dst_dir, name), "wb") as dst:
                dst.write(src.read())
            counts[parts[0]] = counts.get(parts[0], 0) + 1
    for k, v in counts.items():
        print(f"  {k}: {v}")
    if args.no_train:
        return
    for need in (args.rec, args.synth):
        if not os.path.isdir(need):
            sys.exit(f"нет папки {need} — укажите --rec/--synth")
    # длинная запись фона (bg_receiver.py или Google Диск) — скопируйте WAV в kws/bg_long, подхватится тоже
    bg_long = os.path.join(HERE, "bg_long")
    if os.path.isdir(bg_long):
        os.makedirs(os.path.join(HERE, "bg_live"), exist_ok=True)
        import shutil
        for f in os.listdir(bg_long):
            if f.endswith(".wav") and not os.path.exists(os.path.join(HERE, "bg_live", f)):
                shutil.copy(os.path.join(bg_long, f), os.path.join(HERE, "bg_live", f))
    cmd = [sys.executable, "-I", os.path.join(HERE, "train.py"), "--rec", args.rec, "--synth", args.synth,
           "--hard", os.path.join(HERE, "hard"), "--pos", os.path.join(HERE, "pos_live"), "--bg", os.path.join(HERE, "bg_live"),
           "--out", args.out, "--all", "--thresholds", "0.995,0.98,0.96,0.93,0.9,0.85,0.78,0.7,0.6,0.5"]
    print("обучаю:", " ".join(cmd))
    subprocess.run(cmd, check=True, cwd=HERE)
    print(f"готово: {os.path.join(args.out, 'wake.bin')} — загрузите его в настройках («Обучение»)")


if __name__ == "__main__":
    main()
