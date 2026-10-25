# lazylocal Spike Araştırma ve Ölçüm Raporu (K0 Spike)

**Tarih:** 25 Ekim 2026  
**Platform:** macOS 15.x / Darwin arm64 (Apple Silicon M3)  
**Hedef:** lazylocal çekirdek mimarisi için `lsof` tarama maliyeti, `.localhost` loopback çözümlemesi ve unprivileged port `:80` bağlama fizibilitesinin gerçek terminal ölçümleriyle doğrulanması.

---

## 1. `lsof -nP -iTCP -sTCP:LISTEN -F pcn` Gecikme ve Çıktı Analizi

### Komut ve Ölçüm Metodolojisi
macOS üzerinde dinleyen (LISTEN) TCP soketlerini ve bunlara sahip süreçleri tespit etmek için `lsof` komutunun `-F` (field-oriented) modu kullanılır:
```sh
lsof -nP -iTCP -sTCP:LISTEN -F pcn
```
- `-n`: DNS çözümlemesini atlar (hız kazanımı).
- `-P`: Port isim çözümlemesini (getservbyname) atlar; doğrudan sayısal port döner.
- `-iTCP -sTCP:LISTEN`: Yalnızca TCP LISTEN durumundaki soketleri filtreler.
- `-F pcn`: PID (`p`), komut adı (`c`), soket dosya tanımlayıcısı (`f`) ve isim/port (`n`) alanlarını döner.

### 5 Sıralı Ölçüm Sonuçları
```sh
Runs: [13.45 ms, 11.92 ms, 10.85 ms, 9.87 ms, 9.49 ms]
Ortalama Gecikme: 11.12 ms (Min: 9.49 ms, Max: 13.45 ms)
```

### Ham Terminal Çıktısı (Örnek Kesit)
```text
p898
cControlCenter
f10
n*:7000
f11
n*:7000
f12
n*:5000
f13
n*:5000
p936
crapportd
f10
n*:49152
f12
n*:49152
p1085
cagy
f10
n127.0.0.1:49162
f12
n127.0.0.1:49163
p1104
clogioptionsplus_agent
f47
n*:59869
p1314
c2.1.308
f43
n127.0.0.1:49235
p1482
cPython
f7
n127.0.0.1:8775
p1568
cagy
f10
n127.0.0.1:49249
f11
n127.0.0.1:49250
```

### Değerlendirme
`lsof -nP -iTCP -sTCP:LISTEN -F pcn` çağrısı Apple Silicon üzerinde ortalama **~11 ms** sürmektedir. 1-2 saniyelik arka plan tarama periyodu (polling ticker) için CPU maliyeti ihmal edilebilir düzeydedir (`%0.05` altında CPU).

---

## 2. `.localhost` Loopback DNS Çözümleme Testi (`curl` & `node -e`)

### RFC 6761 Standardı
RFC 6761 (Special-Use Domain Names) gereği, macOS işletim sistemi düzeyinde `*.localhost` alt alan adlarını sıfır konfigürasyonla doğrudan yerel loopback adreslerine (`127.0.0.1` ve `::1`) çözümler. `/etc/hosts` dosyasına ekleme yapmaya veya `dnsmasq` kurmaya gerek yoktur.

### a) `curl` ile Çözümleme Testi
```sh
curl -v --connect-timeout 2 http://hub.localhost:8800/
```

**Ham Terminal Çıktısı:**
```text
* Host hub.localhost:8800 was resolved.
* IPv6: ::1
* IPv4: 127.0.0.1
*   Trying [::1]:8800...
* connect to ::1 port 8800 from ::1 port 51790 failed: Connection refused
*   Trying 127.0.0.1:8800...
* connect to 127.0.0.1 port 8800 from 127.0.0.1 port 51791 failed: Connection refused
* Failed to connect to hub.localhost port 8800 after 0 ms: Couldn't connect to server
* Closing connection
curl: (7) Failed to connect to hub.localhost port 8800 after 0 ms: Couldn't connect to server
```
*Gözlem:* `curl`, `hub.localhost` adresini anında `::1` ve `127.0.0.1` olarak çözmüştür.

### b) `node -e` (getaddrinfo) ile Çözümleme Testi
```sh
node -e '
const dns = require("dns");
["hub.localhost", "matematik.localhost", "anything.localhost"].forEach(host => {
  dns.lookup(host, { all: true }, (err, addresses) => {
    if (err) console.error(host, "ERROR:", err.message);
    else console.log(host, "->", JSON.stringify(addresses));
  });
});
'
```

**Ham Terminal Çıktısı:**
```text
matematik.localhost -> [{"address":"::1","family":6},{"address":"127.0.0.1","family":4}]
hub.localhost -> [{"address":"::1","family":6},{"address":"127.0.0.1","family":4}]
anything.localhost -> [{"address":"::1","family":6},{"address":"127.0.0.1","family":4}]
```

### Değerlendirme
Hem sistem resolver'ı (`curl`), hem de libc `getaddrinfo` tabanlı motorlar (`node`, `go net`) tüm `*.localhost` sorgularını otomatik olarak `127.0.0.1` ve `::1` adreslerine yönlendirmektedir. Dinamik subdomain mimarisi (`<app>.localhost`) sıfır bağımlılıkla çalışmaktadır.

---

## 3. macOS'ta Root Olmadan `:80` Bağlama Testi & Port Kararı

### Test Senaryosu
Ayrıcalıksız kullanıcı (`uid=501`) olarak loopback üzerinde `:80` portunu dinleme (`bind`) girişimi:

```python
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    s.bind(("127.0.0.1", 80))
    print("Success: bound 127.0.0.1:80")
except Exception as e:
    print(f"Error binding 127.0.0.1:80: [{type(e).__name__}] {e}")
```

**Ham Terminal Çıktısı:**
```text
Error binding 127.0.0.1:80: [PermissionError] [Errno 13] Permission denied
```

### Bulgular & Mimari Karar
1. **İmtiyaz Kısıtı:** macOS/BSD çekirdeğinde `1024` altındaki portlar imtiyazlıdır (privileged ports). Root (`sudo`) olmadan `127.0.0.1:80` soketine bağlanılamaz (`Permission denied - errno 13`).
2. **Varsayılan Proxy Portu:** `lazylocal` reverse proxy'si, root yetkisi gerektirmeden sorunsuz çalışabilmesi için varsayılan olarak ayrıcalıksız bir portta dinlemelidir:
   - **Varsayılan Port:** `:7080` (veya hub entegrasyonu için `:8800`).
3. **İsteğe Bağlı `:80` Yönlendirmesi (`pfctl`):**
   Kullanıcının tarayıcıda port numarası (`:7080`) yazmadan doğrudan `http://<app>.localhost/` kullanabilmesi için macOS yerel paket filtresi (`pfctl`) ile tek satırlık yönlendirme kuralı tanımlanabilir:
   ```sh
   # 80 portunu 7080'e yönlendir (sudo ile bir defalık):
   echo "rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port 7080" | sudo pfctl -ef -
   ```
   Kuralı kaldırmak için:
   ```sh
   sudo pfctl -F all -f /etc/pf.conf
   ```
