# Авторы и благодарности

YaJarvis появился благодаря контрибьюторам проекта, а также открытым проектам и исследованиям сообщества. Ниже указано, кто помогал в разработке и какие сторонние работы использовались как основа, ориентир или техническая документация. Авторство сторонних компонентов сохраняется за их создателями.

## Контрибьюторы YaJarvis

| Автор | Вклад |
| :--- | :--- |
| **[i7d3v3l0p3r](https://github.com/i7d3v3l0p3r)** | Полный перенос веб-панели и установщика на SvelteKit, доработка установщика для rootfs только для чтения, профиль Firefox для VLESS REALITY и улучшения плагина «Обучение слова». |
| **[Renat Sharipov (SHAREN)](https://github.com/SHAREN)** | Фильтрация поддерживаемых уровней размышления текстовых моделей в интерфейсе. |

## Яндекс Станции и интерфейс

| Автор | Проект | Что использовано |
| :--- | :--- | :--- |
| **dimap** | [yapanel](https://github.com/dimap/yapanel-web) | Визуальный ориентир веб-панели: тёмная тема, оранжевый акцент, карточки состояния, графики и музыкальный интерфейс. Атрибуция также сохранена в заголовке `jarvisd/ui/src/app.css`. |
| **Marker284 и участники сообщества** | [Yandex Station Extended](https://github.com/Marker284/yandex-station-extended), [hackalice.me](https://hackalice.me/) | Исследования root-доступа, устройства прошивок, аудиотракта, подсветки и аппаратных различий Станций. |
| **rison1337** | [Alisa ChatGPT Voice Assistant](https://github.com/rison1337/Alisa-ChatGPT-voice-assistant) | Отправная точка ранних экспериментов с ChatGPT Voice на Станции и подключением аудио колонки. |
| **amirilivice** | [quasard](https://github.com/amirilivice/quasard) | Ориентир на раннем этапе работы с собственным голосовым бэкендом Станции. Текущая программа YaJarvis работает непосредственно на колонке. |
| **tangenx** | [station-wakeword](https://github.com/tangenx/station-wakeword) | Технический ориентир для локальной активации и подхода с признаками openWakeWord. Чужие наборы аудиозаписей не включены в репозиторий. |
| **Morvex885** | [Pulsar](https://github.com/Morvex885/pulsar) | Документация аппаратной части и штатного распознавателя коротких команд Станции. |
| **nyakokitsu** | [YAM](https://github.com/nyakokitsu/yam) | Документация идентификаторов аппаратных платформ и особенностей определения моделей. |

Перечисленные проекты не являются авторами или официальными сопровождающими YaJarvis. Описание вклада не означает, что каждый проект целиком включён в поставку.

## Распознавание, звук и инфраструктура

В качестве визуальных референсов движения установщика использовалась подборка [DesignBookmark Motion](https://designbookmark.com/motion): переходы шагов, отклик кнопок и смена состояний. Реализация использует CSS и Web Animations API вместе с анимациями самой панели YaJarvis.

| Авторы | Компонент | Роль |
| :--- | :--- | :--- |
| **dscripka и участники openWakeWord; Google** | [openWakeWord](https://github.com/dscripka/openWakeWord) | Мел-признаки и speech embedding для локального слова активации. |
| **Команды yt-dlp и EJS** | [yt-dlp/ejs](https://github.com/yt-dlp/ejs) | Подход к обработке скрипта YouTube и расшифровке параметров аудиоссылок. |
| **Bellard, Gordon и участники rquickjs** | [QuickJS](https://bellard.org/quickjs/), [rquickjs](https://github.com/DelSkayn/rquickjs) | Выполнение скрипта плеера YouTube на колонке. |
| **Команда Lua и участники mlua** | [Lua 5.4](https://www.lua.org/), [mlua](https://github.com/mlua-rs/mlua) | Песочница и API для лёгких плагинов. |
| **XTLS и участники Xray-core** | [Xray-core](https://github.com/XTLS/Xray-core) | VLESS / REALITY и сетевой транспорт. |
| **Команда rustls и участники uTLS** | [rustls](https://github.com/rustls/rustls), [uTLS](https://github.com/refraction-networking/utls) | TLS-стек и отпечатки браузеров для VPN-модуля. |
| **Участники webrtc-rs** | [webrtc-rs](https://github.com/webrtc-rs/webrtc) | WebRTC-транспорт в голосовых сессиях ChatGPT. |
| **Xiph.Org Foundation** | [Opus](https://opus-codec.org/) | Кодирование и декодирование аудио Opus. |
| **Michael Sharanda и участники Manrope** | [Manrope](https://github.com/google/fonts/tree/main/ofl/manrope) | Шрифт веб-панели. |
| **Lucide contributors** | [Lucide](https://github.com/lucide-icons/lucide) | SVG-контуры иконок панели. |

Тексты лицензий и сведения о зависимостях находятся в [THIRD_PARTY.md](THIRD_PARTY.md) и [LICENSES](LICENSES). Имена, уведомления об авторстве и лицензии сохраняются вместе с соответствующими компонентами.
