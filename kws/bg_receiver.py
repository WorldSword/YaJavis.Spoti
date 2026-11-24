"""Приёмник записи фона с колонки (настройки → Безопасность → Обучение → «Долгая запись фона» → «Свой сервер»).

    python kws/bg_receiver.py [--dir kws/bg_long] [--port 8765]

Покажет адрес вида http://192.168.x.x:8765/ — вставьте его в настройках колонки.
Колонка присылает каждую минуту звука файлом WAV (HTTP PUT). Потом обучение: from_speaker.py
подхватит эту папку как фон (--bg), или укажите её train.py напрямую: --bg kws/bg_long
"""
import argparse, http.server, os, re, socket

HERE = os.path.dirname(os.path.abspath(__file__))


def lan_ip():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("192.168.0.1", 1))  # пакет не уходит — так узнаём адрес в домашней сети
        return s.getsockname()[0]
    except OSError:
        return "127.0.0.1"
    finally:
        s.close()


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dir", default=os.path.join(HERE, "bg_long"))
    ap.add_argument("--port", type=int, default=8765)
    args = ap.parse_args()
    os.makedirs(args.dir, exist_ok=True)

    class H(http.server.BaseHTTPRequestHandler):
        def do_PUT(self):
            name = os.path.basename(self.path.split("?")[0])
            n = int(self.headers.get("Content-Length") or 0)
            if not re.fullmatch(r"[\w.-]+\.wav", name) or not 0 < n <= 20 << 20:
                self.send_response(400); self.end_headers(); return
            data = self.rfile.read(n)
            if name != "jarvis-bg-check.wav":
                with open(os.path.join(args.dir, name), "wb") as f:
                    f.write(data)
                print(f"  {name}  {n / 1048576:.1f} МБ")
            self.send_response(201); self.end_headers()

        do_POST = do_PUT

        def do_MKCOL(self):
            self.send_response(201); self.end_headers()

        def log_message(self, *a):
            pass

    print(f"Жду записи фона. Адрес для настроек колонки: http://{lan_ip()}:{args.port}/")
    print(f"Файлы — в {args.dir}. Остановить: Ctrl+C")
    http.server.ThreadingHTTPServer(("0.0.0.0", args.port), H).serve_forever()


if __name__ == "__main__":
    main()
