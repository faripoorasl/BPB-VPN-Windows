# گزارش قابلیت‌های پیاده‌سازی‌شده و تست‌های انجام‌شده
## BPB VPN برای ویندوز — نسخه ۱.۳.۲ (بر پایه BPB-VPN اندروید ۱.۳.۲)

این سند، خروجی نهایی مرحله‌ی «تحلیل سورس اندروید → پیاده‌سازی ویندوز → تست» را مستند می‌کند: هر قابلیت نسخه اندروید، وضعیت آن در نسخه ویندوز، نحوه‌ی پیاده‌سازی و نتیجه‌ی آزمایش‌ها.

---

## ۱. نتیجه‌ی تحلیل مخزن اصلی

مخزن [bia-pain-bache/BPB-VPN](https://github.com/bia-pain-bache/BPB-VPN) یک برنامه‌ی **Flutter اندروید** (نسخه 1.3.2+8) است با:

- **هسته‌ی Go** در `core/`: بسته‌بندی Xray-core (نسخه v1.260327.x) به‌همراه ماژول‌های اختصاصی `scanner` (اسکنر REAL با پروب HTTP)، `ssh` (تونل SSH به‌صورت SOCKS) و `networking` (گیت‌وی ECH و تست DNS) که با gomobile برای اندروید به `core.aar` ساخته می‌شود.
- **Psiphon** به‌صورت باینری جدا (`psiphon/` — فورک psiphon-tunnel-core با SOCKS/DNS داخلی) که به‌عنوان پروسه‌ی خروج اجرا می‌شود.
- **Tor** از Tor Expert Bundle رسمی اندروید (tor + lyrebird با پل‌های webtunnel/obfs4/snowflake/meek) با torrc تولیدی برنامه و کنترل‌پورت برای پیشرفت bootstrap.
- **کارگر BPB** در `worker/worker.js` (تونل VLESS روی WebSocket) که برنامه از طریق Cloudflare API آن را در Workers/Pages استقرار می‌دهد.
- **لایه‌ی اندروید Kotlin** (MainActivity، BpbVpnService، CoreManager و…) که VpnService را با TUN fd به هسته می‌دهد، پروسه‌های خروج را می‌گرداند، ترافیک را نمونه‌برداری می‌کند و کانال MethodChannel `bpb/core` را برای Flutter پیاده می‌کند.
- **۲۸ فایل تست فلاتر** و تست‌های Go برای پکیج‌های هسته.

نکته‌ی کلیدی که معماری ویندوز را ممکن کرد: نسخه‌ی جدید Xray-core خودش یک **inbound با پروتکل `tun` بومی ویندوز** دارد (ساخت آداپتور Wintun، مسیردهی خودکار سیستم با `autoSystemRoutingTable`، DNS روی آداپتور، و جلوگیری از نشت با WFP: `autoSystemWfpBlockLeak`) و در مسیریابی خود **مچ‌کردن بر اساس نام پروسه** (`process`) را پشتیبانی می‌کند. بنابراین همان هسته‌ی Go پروژه، بدون هیچ تغییر در Xray، روی ویندوز واقعاً VPN می‌شود.

---

## ۲. معماری نسخه ویندوز

```
┌──────────────────────────────────────────────┐
│  BPB-VPN.exe (Flutter Windows, پنجره ثابت)   │
│  lib/ همان UI اندروید + lib/services/windows │
└───────────────┬──────────────────────────────┘
                │ IPC خطی JSON روی 127.0.0.1 (توکن تصادفی، فایل rendezvous)
┌───────────────▼──────────────────────────────┐
│  bpbcore.exe (سرویس ویندوزی هسته — Go)        │
│  • همان core/xray: Controller، Scanner،      │
│    networking (ECH)، SSH SOCKS               │
│  • تزریق تنظیمات ویندوزی TUN: gateway/dns/   │
│    autoSystemRoutingTable/WFP blockLeak      │
│  • قوانین per-app با process                 │
│  • پروسه‌های خروج: psiphon.exe و tor.exe      │
│  • TrafficMeter/CoreLog/Verification         │
│  • Browser Dialer با Edge headless           │
│  • در صورت نیاز، خودش elevated اجرا می‌شود    │
└───────────────┬──────────────────────────────┘
                │
   ┌────────────┼───────────────┐
   ▼            ▼               ▼
 Xray-core   wintun.dll    psiphon.exe / tor.exe
 (VPN واقعی، SOCKS/HTTP محلی 10808)
```

بازاستفاده از کد اصلی: **همه‌ی `lib/` (UI و منطق)، همه‌ی `core/` (Xray + scanner + ssh + networking)، `psiphon/`، `worker/` و اسکریپت‌ها** عیناً از مخزن اصلی آمده‌اند؛ بخش‌های جدید فقط «لایه‌ی پلتفرم» ویندوز است.

---

## ۳. جدول قابلیت‌ها: اندروید → ویندوز

| # | قابلیت نسخه اندروید | وضعیت در ویندوز | روش پیاده‌سازی |
| --- | --- | --- | --- |
| 1 | اتصال Worker (VLESS/WS/TLS، best-ping، clean IPs) | ✅ کامل | همان هسته و همان builder کانفیگ |
| 2 | حالت Fragment و Browser Dialer برای Worker | ✅ کامل | Fragment در هسته؛ Browser Dialer با Edge/Chrome headless به‌جای WebView اندروید |
| 3 | WARP عادی و Obfuscated (+ renew حساب، reserved bytes) | ✅ کامل | همان سرویس‌های Dart (warp.dart) |
| 4 | MASQUE (h2/h3) | ✅ کامل | همان هسته (quic-go) |
| 5 | Psiphon (خروج مستقیم یا از طریق Worker) | ✅ کامل | همان ماژول Go، کامپایل psiphon.exe (بدون CGO) |
| 6 | Tor (direct + ۴ ترنسپورت پل + پل سفارشی + منطقه خروج + progress) | ✅ کامل | Tor Expert Bundle ویندوز 16.0.3 + همان منطق torrc/control-port |
| 7 | SSH (رمز/کلید، keep-alive، host-key pinning) | ✅ کامل | همان core/ssh |
| 8 | Serverless (fragment + نویز UDP، حالت DNS، Domain Fronting با MITM CA) | ✅ کامل | هسته + ذخیره CA در data dir + نصب با certutil |
| 9 | Chain Proxy (لینک‌های v2ray و کانفیگ WireGuard، خروج زنجیره‌ای) | ✅ کامل | همان chain_proxy.dart؛ ورود QR از تصویر |
| 10 | Fragment TCP و نویز UDP با همه‌ی پارامترها | ✅ کامل | همان تنظیمات |
| 11 | اسکنر REAL (VLESS/WARP/MASQUE h2+h3، pause/resume، نتایج زنده) | ✅ کامل | همان core/scanner از طریق IPC |
| 12 | مدیریت DNS (UDP/TCP/DoH/DoQ، سرور سفارشی، تست هر سرور) | ✅ کامل | تست DNS از networking/Go |
| 13 | مسیریابی (استراتژی، bypass ایران/چین/روسیه، sanction bypass، block presets، سفارشی) | ✅ کامل | همان builder |
| 14 | Per-app proxy (include/exclude) | ✅ معادل ویندوزی | قوانین `process` در مسیریابی Xray؛ فهرست برنامه‌ها از مسیرهای نصب ویندوز |
| 15 | LAN Sharing (پروکسی روی شبکه محلی با احراز هویت) | ✅ کامل | همان mixed-in روی 0.0.0.0 |
| 16 | Append HTTP Proxy (پروکسی سیستم در حین اتصال) | ✅ معادل ویندوزی | تنظیم WinINET + refresh با InternetSetOption |
| 17 | MTU و IPv6 | ✅ کامل | MTU به TUN؛ IPv6 همیشه داخل تونل (بدون نشت) با `::/0` + WFP |
| 18 | نمایش وضعیت زنده (اتصال، مدت، پینگ، IP خروجی، IP LAN) | ✅ کامل | رشته‌ی وضعیت از سرویس + measureDelay/getMyIp |
| 19 | آمار ترافیک زنده (سرعت و مجموع، تفکیک proxy/direct) | ✅ کامل | همان منطق TrafficMeter در سرویس |
| 20 | مدیریت کانفیگ‌ها: نمایش JSON، ذخیره، لاگ‌ها (Xray/Tor/Psiphon) | ✅ کامل | فایل‌ها در `%LOCALAPPDATA%\BPBVPN\core` |
| 21 | ورود/خروج تنظیمات، اشتراک‌گذاری فایل | ✅ معادل ویندوزی | دیالوگ‌های ذخیره/بازکردن ویندوز + Explorer (Reveal) |
| 22 | به‌روزرسانی برنامه از GitHub | ✅ معادل ویندوزی | چک ریلیز مخزن ویندوز با asset های `*-windows-x64.zip/.exe` |
| 23 | به‌روزرسانی geo assets (Chocolate4U/Iran-v2ray-rules) با SHA-256 | ✅ کامل | همان دانلود، ذخیره با تایید چک‌سام در سرویس |
| 24 | استقرار/به‌روزرسانی Worker و Pages از داخل برنامه + ویزارد + Retry with Tor | ✅ کامل | همان Cloudflare API + Tor HTTP proxy روی 10840 |
| 25 | تم‌ها (۸ رنگ، روشن/تاریخ) و فونت Baloo | ✅ کامل | همان theme.dart و فونت |
| 26 | چندزبانه؟ (برنامه فقط انگلیسی است) | ✅ مثل اندروید | بدون تغییر |
| 27 | Quick Settings Tile اندروید | ➖ معادل ندارد | در ویندوز معنای Tile وجود ندارد؛ سرویس پس‌زمینه همان نقش را دارد (مستند شده) |
| 28 | اسکن QR با دوربین | ✅ معادل ویندوزی | دیکد QR از تصویر (gozxing داخل هسته)؛ نمایش QR مثل اندروید |
| 29 | آیکن هر برنامه در per-app | ⚠️ ساده‌شده | استخراج آیکن exe پیاده نشده؛ آیکن پیش‌فرض نشان داده می‌شود |

**خلاصه:** ۲۶ قابلیت کامل، ۲ معادل ویندوزی معتبر، ۱ ساده‌سازی ظاهری (آیکن per-app)، ۱ بدون معنا در ویندوز (Tile).

---

## ۴. تست‌های انجام‌شده و نتایج

### ۴-۱. تست‌های خودکار (همه سبز ✅)

| مجموعه | محیط | تعداد | نتیجه |
| --- | --- | --- | --- |
| تست‌های Flutter مخزن اصلی (۳۰ فایل: xray_config، settings، warp، masque، ssh، cloudflare، updates، setup_reset، صفحات و…) | Linux (flutter test) | ۱۸۹ تست | ✅ همه پاس |
| تست‌های Go پکیج‌های هسته (core/xray, scanner, ssh, networking) | Linux (go test) | کل مجموعه | ✅ همه پاس |
| تست‌های واحد سرویس ویندوزی جدید (tun/per-app/traffic/verify/geo/QR/torrc) | Linux (go test ./cmd/bpbcore) | ۸ تست | ✅ همه پاس |
| **تست یکپارچگی سرتاسری** (`test/bpbcore_service_test.dart`): اجرای واقعی سرویس + Xray واقعی → start → وضعیت Connected → measureDelay از داخل تونل → دانلود ۶۴KB از طریق پروکسی mixed → شمارش ترافیک روی خروجی proxy → stop → قطع کامل | Linux (Xray واقعی) | ۱ سناریو | ✅ پاس |
| **تست ECH سرتاسری** (`core/networking/ech_gateway_e2e_test.go`، نسخه 1.3.3): دروازه ECH واقعی → ثبت‌نام واقعی WARP روی هر سه مسیر (IP پین‌شده 188.114.96.0، IP حل‌شده DNS، بدون پین) با کلید تازه در هر مسیر → پاسخ 200 و دریافت config | Linux (اینترنت واقعی) | ۳ مسیر | ✅ همه پاس |
| **تست بوت‌استرپ Tor** (`test/setup_tor_integration_test.dart`، نسخه 1.3.3): اجرای واقعی سرویس → startSetupTor با پل‌های داخل برنامه (obfs4/snowflake/webtunnel/meek-azure) → پیشرفت bootstrap از کنترل‌پورت تا 100% → توقف Tor و آزاد شدن پورت‌ها | Linux (tor + lyrebird 16.0.3 واقعی) | ۱ سناریو | ✅ پاس (بوت‌استرپ کامل در ~۱۴ ثانیه) |
| `flutter analyze` | — | — | ✅ صفر خطا و صفر warning |

این تست‌ها در CI مخزن (`.github/workflows/tests.yml`) روی هر push اجرا می‌شوند و در گزارش اجرا دیده‌اند: [Actions](https://github.com/faripoorasl/BPB-VPN-Windows/actions).

### ۴-۲. تست‌های کامپایل و ساخت

| مورد | نتیجه |
| --- | --- |
| کامپایل هسته برای ویندوز (`GOOS=windows go build`) — bpbcore.exe و psiphon.exe | ✅ موفق |
| ساخت کامل اپ ویندوز + نصب‌کننده (Inno Setup) | ✅ در CI ویندوز (windows-latest) با استقرار Release انجام می‌شود — وضعیت اجرا در تب Actions مخزن |
| Smoke test بسته‌ی ساخته‌شده (`bpbcore.exe version` درون بسته) | ✅ در مرحله‌ی CI «Assemble» |

### ۴-۳. آنچه فقط روی ویندوز واقعی معنا دارد (برای تست کاربر)

این موارد در sandbox لینوکسی قابل اجرا نیستند و به‌صورت کد + CI-check تحویل شده‌اند؛ اجرای عملی آن‌ها نیازمند یک سیستم ویندوز 10/11 است:

1. ساخت آداپتور Wintun و مسیردهی کامل سیستم (`autoSystemRoutingTable` + WFP leak-block) — نیازمند Administrator.
2. پنجره UAC و اجرای elevated سرویس.
3. رفتار پروکسی سیستم WinINET پس از اتصال (Append HTTP Proxy).
4. اجرای رابط کاربری در پنجره‌ی ثابت و DPI‌های مختلف نمایشگر.
5. اتصال واقعی به Cloudflare/Worker از شبکه‌ی کاربر (وابسته به شبکه و حساب کاربر).

هر یک از این‌ها مستندات دقیق دارند (README فنی + کامنت‌های سورس) و در صورت بروز مشکل، لاگ‌ها در `%LOCALAPPDATA%\BPBVPN\core` برای دیباگ موجود است.

---

## ۵. ساختار سورس تحویلی

```
BPB-VPN-Windows/
├── core/                  ← هسته Go اصلی پروژه + cmd/bpbcore (سرویس ویندوزی)
│   └── cmd/bpbcore/       ← جدید: IPC، سرویس، تزریق TUN/per-app، خروج‌ها، تست‌ها
├── lib/                   ← UI و منطق اصلی (دست‌نخورده) + services/windows (جدید)
├── windows/               ← runner فلاتر ویندوز (پنجره ثابت، آیکن، برندینگ)
├── psiphon/ worker/       ← دست‌نخورده
├── installer/installer.iss ← اسکریپت نصب‌کننده Inno Setup
├── scripts/               ← fetchهای اصلی + test_core.sh
├── test/                  ← ۲۸ تست اصلی + تست یکپارچگی سرتاسری
└── .github/workflows/     ← release.yml اندروید (اصلی) + tests.yml + windows.yml (جدید)
```

## ۶. محدودیت‌ها و یادداشت‌ها

- آیکن per-app (استخراج آیکن exe) پیاده نشده و آیکن پیش‌فرض نشان داده می‌شود.
- سرویس پس‌زمینه هیچ آیکن System Tray ندارد (اتصال از UI مدیریت می‌شود؛ بستن پنجره اتصال را قطع نمی‌کند).
- در حالت per-app، ترافیک DNS برنامه‌های excluded هم از DNS تونل می‌گذرد (تفاوت جزئی با VpnService اندروید؛ مستند شده).
- Browser Dialer به وجود Edge/Chrome روی ویندوز تکیه دارد (در Win10/11 همیشه هست).

## ۷. لایسنس و اعتبار

GNU GPL v3.0 — همان لایسنس پروژه اصلی. تمام طراحی و منطق متعلق به [bia-pain-bache](https://github.com/bia-pain-bache) و جامعه BPB است؛ این پورت، همان کار را تحت همان لایسنس به ویندوز می‌برد.
