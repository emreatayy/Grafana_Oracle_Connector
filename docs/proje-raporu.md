# Grafana ↔ Oracle Veri Kaynağı Eklentisi — Kapsamlı Tasarım ve Analiz Raporu

**Tarih:** 2026-08-06
**Referans proje:** https://github.com/albertowd/oracle-grafana (Apache-2.0)
**Kapsam:** Mevcut eklentinin eksikleri üzerine, Oracle + Prometheus çift modlu, alarm destekli yeni bir Grafana data source eklentisinin tasarımı.

---

## 1. Yönetici Özeti

`albertowd/oracle-grafana` projesi, Oracle veritabanına bağlanıp `SELECT` sorgularını Grafana panellerine dönüştüren, Go backend + TypeScript frontend yapısında bir Grafana data source eklentisidir. Temel fikir doğru ve çalışıyor; ancak üç yapısal eksiklik var:

1. **Zaman serisi (timeseries) üretemiyor.** Tüm sütunları `string`'e çevirip Grafana'nın beklediği `time.Time` alanını hiç üretmiyor. Bu yüzden `timeseries` grafiği çizilemiyor.
2. **Prometheus desteği yok.** Veri doğrudan Oracle'dan çekiliyor; yüksek frekanslı, uzun süreli, aggregasyon gerektiren senaryolar için ölçüm aracısı (Prometheus) yok.
3. **Alarm üretmiyor.** Grafana alarm kuralı, zaman serisi dönen sorgular üzerinden değerlendirilir; zaman serisi olmayınca alarm da olmuyor.

Bu rapor; (a) mevcut kodu satır satır inceleyip kök nedenleri çıkarır, (b) **doğrudan Oracle (SQL) modu + isteğe bağlı Prometheus (PromQL) modu** çift modlu bir mimari önerir, (c) alarm altyapısını tasarlar ve (d) gerçekçi bir uygulama yol haritası sunar.

**Temel karar:** Zaman serisi ve alarm için Prometheus **zorunlu değildir** — doğru tip haritalaması yapan bir eklenti bunları Oracle'dan doğrudan da üretebilir. Prometheus, bir **opsiyonel katman** olarak eklenir: kullanıcı, sorgu editöründeki bir buton/sekme ile hangi kaynağı kullanacağını seçer.

---

## 2. Mevcut Projenin Analizi (`albertowd/oracle-grafana`)

### 2.1 Mimari Genel Bakış

```
Grafana (panel / alert rule / variable)
        │  gRPC (plugin SDK protokolü)
        ▼
┌─────────────────────────────── Grafana Sunucusu ───────────────────────────────┐
│  Backend Plugin (Go)  ── gpx_oracle_grafana executable                        │
│   datasource.Manage(...)                                                      │
│   ├── QueryDataHandler  →  OracleDatasource.query(...)                         │
│   ├── CheckHealthHandler → test bağlantısı                                     │
│   └── instancemgmt (veri kaynağı başına 1 instance)                            │
│        │  database/sql + github.com/sijms/go-ora/v2 (pure Go sürücü)           │
│        ▼                                                                       │
│     Oracle DB  (host:port/service  veya  connString / SID)                     │
└────────────────────────────────────────────────────────────────────────────────┘
                        │  HTTP (browser)
┌────────────────────────────────────────────────────────────────────────────────┐
│  Frontend (TypeScript/React)                                                  │
│   datasource.ts  → DataSourceWithBackend<MyQuery, MyDataSourceOptions>          │
│   interpolate.ts → $__from / $__to ve $degisken yerine koyma                    │
│   QueryEditor.tsx→ SQL yazma + "parsed query" önizleme (iki metin kutusu)      │
│   ConfigEditor.tsx→ user/password/connString/host/port/service/sid             │
└────────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Teknoloji Yığını (doğrulanmış)

| Katman | Teknoloji | Not |
|---|---|---|
| Backend | Go 1.19 + `grafana-plugin-sdk-go v0.156.0` | SDK 2023 başı sürümü; modern sürümler çok daha yetkin |
| Oracle sürücü | `sijms/go-ora/v2 v2.7.9` | **Pure Go** — Oracle Instant Client/OCI gerekmez, büyük dağıtım avantajı |
| Frontend | TypeScript + React + Grafana UI kit | SDK tabanlı |
| Test | Jest + Cypress (smoke) | Backend için Go unit test (`datasource_test.go`) |
| Dev ortamı | `docker-compose.yaml`: `grafana/grafana:latest` + Oracle XE 21.3 | Startup SQL scriptleriyle demo verisi yükleniyor |

### 2.3 Güçlü Yönler

- **Kurulumu basit:** tek `.tar.gz`, `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS` ile yükleniyor.
- **Pure Go Oracle sürücüsü:** istemci tarafı OCI bağımlılığı yok (godror'a göre büyük kazanç).
- **Değişken desteği:** `$__from`/`$__to` ve `$degisken` interpolasyonu (frontend'de `getTemplateSrv().replace`), "SQL önizleme" özelliği iyi düşünülmüş.
- **Query variable:** Veri kaynağı üzerinden tek sütun dönen SQL ile dropdown değişken tanımlama.
- **Güvenlik bilinci:** şifre `secureJsonData` (decrypted) üzerinden backend'e iletilir, log'da maskeleme var (`********`).
- **Esnek bağlantı:** basit alanlar ya da tek `ConnString` (SID dahil).

### 2.4 Zayıf Yönler ve Kök Nedenleri (kod doğrulamalı)

| # | Sorun | Kök neden (kod) | Etki |
|---|---|---|---|
| 1 | **Zaman serisi üretilemiyor** | `query.go` tüm hücreleri `sql.RawBytes → string(scannedValue)` ile okur; sütun tipini hiç incelemez; `time.Time` alanı üretmez | `timeseries` paneli çalışmaz; yalnızca `TO_CHAR(...)` ile elle string'e çevrilmiş "sahte zaman" var (`example/queries.sql`) |
| 2 | **Sayısal paneller yok** | NUMBER sütunlar bile string döner | stat/gauge/bar için değerler string; aritmetik ve birim config imkânsız |
| 3 | **Alarm üretilemiyor** | Zaman serisi olmadığı için Grafana alarm kuralı değerlendiremiyor | Alarm yok (Grafana Unified Alerting, zaman serisi dönen sorgu bekler) |
| 4 | **NULL bozuluyor** | null → literal `"(null)"` string | analiz/grafikte yanlış değer |
| 5 | **İptal/yayın sınırı yok** | `query(_ context.Context, ...)` bağlamı yok sayar; `stmt.Query()` (ctx'siz) | Uzun süren SQL panel kapatılsa bile Oracle'da çalışmaya devam eder; kaynak sızıntısı |
| 6 | **`$__interval` ve standart makrolar yok** | Sadece `$__from`/`$__to` | Zaman gruplamalı (`GROUP BY` ile bucketing) sorgular elle yazılamaz; panellerde otomatik çözünürlük ayarı yok |
| 7 | **Çok değerli değişken desteklenmiyor** | README'de "future" olarak belirtilmiş | `IN (…)` genişletmesi yok |
| 8 | **Bağlantı havuzu ayarları yok** | `sql.Open` sonrası `SetMaxOpenConns` vb. hiç çağrılmıyor; tek `*sql.DB` | Yüksek panel yoğunluğunda Oracle'a bağlantı patlaması riski |
| 9 | **Tip tespiti / metadata yok** | Backend kolon adlarını sadece string dizi olarak alır | Editörde otomatik tamamlama, time column seçimi, birim eşleme yok |
| 10 | **Prometheus yok** | Doğrudan Oracle sorgusu her panel yenilemesinde canlı SQL çalıştırır | Yüksek frekans, uzun retention, PromQL aggregasyon, Alertmanager entegrasyonu mümkün değil |
| 11 | **Annotations yok** | `plugin.json` içinde `annotations: true` yok; handler yok | Oracle olaylarını panellerde işaretleyemiyor |
| 12 | **Hata kodları ham** | Oracle hata mesajı doğrudan yansıtılıyor | ORA-#### çözümleme, kullanıcı dostu mesaj yok |

---

## 3. Yeni Projenin Gereksinimleri (Vizyon)

### 3.1 Fonksiyonel Gereksinimler

**FR-1 — Çift modlu sorgu editörü (ÇEKİRDEK):**
Kullanıcı, sorgu editöründeki bir sekmeli buton ile **"Oracle (SQL)"** veya **"Prometheus (PromQL)"** modunu seçer. Seçim sorgu bazlı saklanır; veri kaynağı düzeyinde bir varsayılan mod da ayarlanabilir.

**FR-2 — Doğrudan Oracle modu (geliştirilmiş):**
- Oracle sütun tiplerini doğru okuma: `NUMBER→float64`, `DATE/TIMESTAMP→time.Time`, `VARCHAR2/CHAR→string`, `CLOB→string`, `BLOB/RAW→[]byte`, `NULL→veri null`.
- Bir zaman sütunu varsa **otomatik zaman serisi frame** üretimi (Grafana `data.Frame` + `time.Time` alanı).
- "Format as" seçeneği: **Table | Time series | Auto** (MySQL/Postgres SQL veri kaynaklarındaki desen).
- Grafana standart makroları: `$__time(col)`, `$__timeFilter(col)`, `$__timeFrom`, `$__timeTo`, `$__interval`, `$__interval_ms`, `$__unixEpochFilter(col)`, `$__unixEpochGroup(col, interval)` ve `$__from`/`$__to` (mevcut).
- Çok değerli dashboard değişkenlerinin `IN (…)` genişletmesi.
- `context.Context` ile sorgu iptali + `database/sql` havuz parametreleri + satır limiti.

**FR-3 — Prometheus modu (opsiyonel, isteğe bağlı buton):**
- Veri kaynağı config'inde opsiyonel Prometheus bağlantısı (URL, basic auth / token, TLS).
- Backend, PromQL sorgusunu Prometheus HTTP API'sine (`/api/v1/query_range`, `/api/v1/query`) iletir ve sonucu Grafana frame'lerine çevirir.
- Editörde **metric tarayıcı** (`/api/v1/label/__name__/values`) ile Oracle exporter'ının ürettiği `oracle_*`/`oracledb_*` metriklerini seçme.
- Oracle → Prometheus veri akışı için **Oracle'ın resmi `oracle-db-appdev-monitoring` exporter'ı** ile entegrasyon rehberi (bölüm 4.4).

**FR-4 — Alarm destekleri:**
- Her iki modda da dönen zaman serisi üzerinden **Grafana Unified Alerting** kuralı tanımlama (reduce: last/avg/max/min → eşik).
- SQL modunda "tablo/alarm eşiği" kalıbı: koşul sağlanınca satır dönen sorgu → "rows > 0" koşulu ile alarm (tablo modu korunur).
- Alerting bağlamında (dashboard değişkenleri yokken) makroların güvenli biçimde varsayılan değerlere düşmesi.

**FR-5 — Ek iyileştirmeler (kullanıcıya açık "bunları da ekleyebiliriz" listesi):**
1. `annotations: true` ile SQL'den panel anotasyonu (time, title, text sütunları).
2. Editörde tablo/kolon otomatik tamamlama (Oracle metadata sorgularıyla).
3. Sorgu geçmişi / sık kullanılanlar.
4. Kolon bazlı birim ve etiket config (field config otomasyonu).
5. Zengin sağlık kontrolü: bağlantı + `SELECT` yetkisi + Oracle sürümü + Prometheus reachability.
6. ORA-#### hata kodlarının Türkçe/okunur mesaja çevrilmesi.
7. Kendi ölçümleri: `oracle_datasource_query_duration`, `oracle_datasource_errors_total` vb. (`plugin.json` içinde `"metrics": true` zaten var).
8. TLS/Wallet seçenekleri, TNS bağlantı adları.
9. Satır/veri boyutu sınırları ve büyük sonuçlarda akış.

### 3.2 Kullanıcı Hikâyeleri (örnek)

- **Operatör (zamansal):** "Oracle'daki bir sayaç tablosundan son 24 saatlik `timeseries` grafiği çizmek istiyorum." → SQL modu + `$__timeFilter(ts)` + "Format: Time series". Prometheus gerekmez.
- **SRE (yüksek frekans):** "Saniyelik ayrık veriyi 7 gün boyunca saklayıp `rate()`/`sum by()` ile toplamak istiyorum; Oracle'ı her panelde dövmek istemiyorum." → Prometheus modu + `oracle-db-appdev-monitoring` exporter.
- **NOC (alarm):** "Tablospace kullanımı %85'i aşarsa e-posta alarmı." → SQL veya Prometheus modunda zaman serisi sorgusu + Grafana alarm kuralı.
- **Analist (tablo):** "Sorgu sonucunu tablo olarak göster, koşula uyan satırlar olduğunda da bilgilendir." → "Format: Table" + "rows > 0" alarm kalıbı.

---

## 4. Mimari Tasarım

### 4.1 Genel Mimari

```
Grafana (panel / alert rule / variable / annotation)
        │  gRPC (plugin SDK)
        ▼
┌─────────────────────────────── Grafana Sunucusu ───────────────────────────────┐
│  Backend Plugin (Go)                                                          │
│   ├── QueryDataHandler                                                        │
│   │    ├── mode="sql"     → Oracle executor (go-ora, tip haritalama, makrolar)│
│   │    └── mode="prom"    → Prometheus proxy (HTTP API → frame)               │
│   ├── CheckHealthHandler  → Oracle + Prometheus sağlık kontrolü               │
│   ├── AnnotationQueryHandler (ops.)                                           │
│   └── instancemgmt (DS başına instance, ayar değişince dispose)               │
│        │                                  │  HTTPS (API)                      │
│        ▼                                  ▼                                   │
│     Oracle DB                        Prometheus Server                        │
│        (SELECT only)                        ▲                                 │
│                                            │ scrape /metrics (port 9161)      │
│                                      Oracle Metric Exporter                   │
│                            (oracle-db-appdev-monitoring, konteyner)          │
└────────────────────────────────────────────────────────────────────────────────┘
                        │  HTTP
┌────────────────────────────────────────────────────────────────────────────────┐
│  Frontend (React/TS)                                                          │
│   QueryEditor:  [ Oracle (SQL) | Prometheus (PromQL) ]  sekme-butonu          │
│                 SQL editörü (Monaco, otomatik tamamlama) + önizleme           │
│                 PromQL editörü + metric tarayıcı                              │
│   ConfigEditor: Oracle bölümü + (opsiyonel) Prometheus bölümü [aç/kapa]       │
│   datasource.ts: mod'a göre sorguyu backend'e iletir; variable sorguları      │
└────────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 Zaman Serisi Neden Şimdi Mümkün — ve Prometheus'un Gerçek Rolü

Grafana'nın `timeseries` paneli, frame içinde:

- en az bir **`time.Time` alanı** (X ekseni),
- bir veya daha çok **sayısal değer alanı** (Y ekseni),
- isteğe bağlı **`data.Labels`** (seri etiketleri)

bekler. Mevcut eklenti hepsini string'e çevirdiği için hiçbiri sağlanmıyor. Yeni backend bu üçünü doğru ürettiğinde **zaman serisi ve alarm, Prometheus olmadan da çalışır.**

Prometheus modu şu senaryolarda **gerekli** hale gelir:

| İhtiyaç | Doğrudan SQL | Prometheus |
|---|---|---|
| Tablo / stat / gauge panel | ✅ | ⚠️ (PromQL ile zor) |
| Kısa vadeli zaman serisi (saatler-günler) | ✅ (doğru tip haritalamasıyla) | ✅ |
| Uzun retention (haftalar-aylar) + downsampling | ❌ (canlı SQL pahalı) | ✅ |
| `rate()`, `irate()`, `sum by(…)` agregasyonları | ⚠️ (SQL ile elle yazılır) | ✅ yerleşik |
| Saniyelik/dakikalık sabit ayrık veri | ⚠️ (her panelde Oracle yükü) | ✅ scrape tabanlı |
| Alertmanager / Prometheus eko-sistemi | ❌ | ✅ |
| Dashboard yenileme başına DB yükü | Yüksek (her sorgu canlı) | Düşük (scrape aralığında) |

**Tasarım ilkesi:** Mod seçimi kullanıcıya bırakılır; eklenti birini diğerine dayatmaz. Varsayılan mod `sql`, Prometheus bağlantısı yapılandırılınca `prom` modu kullanılabilir hale gelir.

### 4.3 Sütun Tipi Haritalama ve Frame Üretimi (Backend)

```
Oracle veri tipi            →  Go tipi            →  Grafana data.Frame alanı
───────────────────────────────────────────────────────────────────────────
NUMBER                      →  float64            →  *float64 (nullable)
INTEGER / SMALLINT          →  int64              →  *int64
FLOAT / BINARY_DOUBLE/FLOAT →  float64            →  *float64
DATE                        →  time.Time (UTC)    →  *time.Time   ← zaman alanı
TIMESTAMP[±TZ]              →  time.Time (UTC)    →  *time.Time   ← zaman alanı
CHAR / VARCHAR2 / NVARCHAR2 →  string             →  *string
CLOB / NCLOB                →  string (akışlı)    →  *string
BLOB / RAW                  →  []byte             →  *[]byte
BOOLEAN (23c)               →  bool               →  *bool
NULL                        →  null (değil "(null)") → data.Sentinel / NaN
```

**Frame şekillendirme algoritması (mode="sql"):**

1. Sorguyu çalıştır, `*sql.ColumnType` ile her kolonun Oracle tipini oku (`columnType.DatabaseTypeName()`).
2. Yukarıdaki tabloya göre her kolonu Go değerine çöz (`sql.Null*` / `[]byte` + parse).
3. **Auto format:** En az bir `DATE/TIMESTAMP` kolonu varsa:
   - zaman kolonu → `*time.Time` alanı,
   - diğer sayısal kolonlar → değer alanları,
   - kalan string kolonlar → `data.Labels` (eğer kardinalite düşükse) veya ayrı alan.
4. **Time series format:** Kullanıcı zaman kolonunu açıkça seçtiyse (drop-down, metadata'dan dolu) onu kullanır; yoksa ilk timestamp kolonunu seçer.
5. **Table format:** Tüm kolonlar olduğu gibi (zaman da string değil, `time.Time` olarak tabloda görünür).
6. `frame.Meta` içine `DataTopic`, birim bilgisi, `ExecutedQueryString` gibi meta eklenir.

**Prometheus sonucu → frame (mode="prom"):**

- `/api/v1/query_range` (matrix): her seri için `time.Time` + `float64` değerleri; serinin `metric` etiketleri → `data.Labels`. Uzun format.
- `/api/v1/query` (vector): tek zaman noktası → tek satırlık frame (time=now + değer + etiketler); tablo/stat panel için uygun.

### 4.4 Prometheus Entegrasyonu — Seçenekler ve Öneri

**Araştırma bulguları:**

- `iamseth/oracledb_exporter` (topluluk, MIT): 2025-02'de **arşivlendi**, artık bakım yapılmıyor. README kendisi Oracle'ın resmi çözümüne işaret ediyor.
- **`oracle/oracle-db-appdev-monitoring`** (resmi, UPL/MIT, aktif): "Oracle AI Database Metrics Exporter", Prometheus/OTEL formatında metrik dışa aktarır. Çoklu veritabanı, OCI/Azure/HashiCorp Vault ile kimlik doğrulama, TLS/Wallet, Kubernetes manifestleri, hazır Grafana dashboard'ları ve docker-compose demosu içerir. **Önerilen exporter budur.**

**Seçenekler:**

| Seçenek | Açıklama | Avantaj | Dezavantaj | Karar |
|---|---|---|---|---|
| **A. PromQL proxy (eklenti içi)** | Eklentinin backend'i, config'de verilen Prometheus'a `/api/v1/query*` çağrısı yapar, sonucu frame'e çevirir | Tek data source config, tek sorgu editörü, alarm aynı yerde | Sorgu motorunu (PromQL) büyük ölçüde yeniden "sarmalar" | ✅ Önerilen |
| B. Native Prometheus DS'ye yönlendirme | Kullanıcı, Oracle exporter'ını kurar ve Grafana'nın kendi Prometheus datasource'unu kullanır | Sıfır geliştirme | Kullanıcının iki data source yönetmesi gerekir; "tek buton" deneyimi olmaz | ⚠️ Tamamlayıcı |
| C. Exporter'ı eklentiye gömme | Backend'e scrape edilebilir `/metrics` uç noktası koymak | Tek işlem | Backend plugin'ler gRPC üzerinden çalışır, ayrı HTTP port açamaz (Grafana `/metrics`'e toplanır) — mimari olarak uygun değil | ❌ |
| D. Exporter orchestrasyon | Eklenti, exporter konteynerini yönetir/health-check eder | Kolay kurulum | Config karmaşıklaşır; yeni eklentinin sorumluluk alanı genişler | ⚠️ Faz 5 opsiyon |

**Önerilen uçtan uca akış (Seçenek A + resmi exporter):**

1. Kullanıcı, Oracle veri kaynağının yanına bir Prometheus (veya VictoriaMetrics/Mimir — PromQL uyumlu) kurar.
2. `oracle-db-appdev-monitoring` exporter konteynerini, `default-metrics.yaml` + kendi özel metrikleriyle (`custom-metrics.yaml` — SQL + `context` + `metricsdesc`) ayağa kaldırır; Prometheus scrape etsin diye ayarlar.
3. Eklenti config'inde **Prometheus bölümü** açılır: URL, auth (basic/token), TLS doğrulama.
4. Sorgu editöründe "Prometheus (PromQL)" sekmesine geçilir; metric tarayıcıdan `oracle_*` metrikleri seçilir; PromQL yazılır.
5. Backend PromQL'yi Prometheus'a iletir, frame'e çevirir; panel + alarm çalışır.

**Uygulama notu (A):** Backend tarafı için `prometheus/client_golang`'ın `api` paketi veya çıplak `net/http` + `encoding/json` yeterlidir. Prometheus sonucu zaten Grafana'ya benzer "series + labels + timestamps" yapısındadır; çevrim düz ve az kodludur.

### 4.5 Alarm Sistemi Tasarımı

Grafana **Unified Alerting** (Grafana 9+), alarm kurallarını veri kaynağı sorguları üzerinden değerlendirir. Bir data source'un alarm kuralında kullanılabilmesi için sorgunun **zaman serisi** (veya tablo → reduce) dönmesi yeterlidir; eklenti bunu sağladığında alarmlar **otomatik olarak** açılır. Bu yüzden "alarm üretmek" için özel bir protokol gerekmez; üç katman kurarız:

**Katman 1 — Zaman serisi üzerinden Grafana alarmı (birincil, iki modda da):**
- SQL modu: `$__timeFilter(ts)` içeren, "Format: Time series" bir sorgu. Alarm kuralı → `Reduce (last)` → `Is above (eşik)`.
- Prometheus modu: herhangi bir PromQL zaman serisi (örn. `oracle_db_tablespace_used_percent > 85`). Grafana bu ifadeyi anlar (PromQL boolean karşılaştırma satır sayısı üretir — mevcut "rows > 0" kalıbı Prometheus'ta yerleşik).

**Katman 2 — SQL tablo/alarm kalıbı (güvenli geri dönüş):**
- SQL, koşul sağlanınca ≥1 satır, sağlanmayınca 0 satır döndürür (ör. `SELECT ... WHERE kullanım > 85`). Alarm kuralı "query returns data / rows > 0" koşulu kullanır. Tablo formatında da çalışır.

**Katman 3 — Alertmanager entegrasyonu (opsiyonel):**
- Prometheus modunda, Prometheus'un kendi `ALERTS` metriklerini sorgulayıp Grafana'da görüntüleme (Grafana'nın native Alertmanager veri kaynağı ile de sağlanabilir). Eklenti çekirdeğinin dışında tutulur.

**Teknik gereksinimler (backend):**

- `CoreApp.Alerting` bağlamında (dashboard yok) sorguların çalışabilmesi: makro motoru, scopedVars yoksa `$__from`/`$__to` için zaman aralığını `query.TimeRange`'den, değişkenleri varsayılan değere düşürür; bulunamayan değişken için açık hata mesajı döner (sessiz başarısızlık yerine).
- `plugin.json` içinde ayrı bir `alerting: true` bayrağı datasource'lar için gerekli değildir (backend handler yeterlidir); yine de dokümanda belirtilir.
- Alarm sorgularında `IntervalMs`'ten `$__interval` üretilir (panellerle aynı).

### 4.6 Ön Yüz (Query Editor + Config) Tasarımı

**QueryEditor (sorgu editörü) — yeni hali:**

```
┌─────────────────────────────────────────────────────────────────────────┐
│ [ Oracle (SQL) ]  [ Prometheus (PromQL) ]        ← mod sekmesi-butonu    │
├─────────────────────────────────────────────────────────────────────────┤
│  SQL modu:                                                              │
│   SQL editörü (Monaco, sözdizimi renklendirme, tablo/kolon tamamlama)   │
│   Format as: [Auto | Time series | Table]    Time column: [otomatik ▾]  │
│   Önizleme: (makrolar ve değişkenler uygulanmış SQL, salt okunur)       │
│                                                                         │
│  Prometheus modu:                                                       │
│   Metric tarayıcı: [oracledb_tablespace_used_percent ▾] (prefix filtre) │
│   PromQL editörü (sözdizimi renklendirme, fonksiyon yardımı)            │
│   Önizleme: (anlık query_range önizleme mini-grafiği)                    │
└─────────────────────────────────────────────────────────────────────────┘
```

**ConfigEditor — yeni hali:**

```
Oracle Bağlantısı (mevcut alanlar +):  ConnString / host / port / service / sid
                                       user / password (secure) / TLS seçenekleri
─────────────────────────────────────────────────────────────────────────
Prometheus Bağlantısı   [ X ] "Prometheus kullan" (opsiyonel, aç/kapa butonu)
  URL, Basic auth (user/pass → secure), Bearer token (→ secure), TLS verify
  Oracle exporter bilgisi (opsiyonel referans: "oracle-db-appdev-monitoring")
─────────────────────────────────────────────────────────────────────────
Genel:  varsayılan mod, sorgu zaman aşımı, max satır, birim/etiket kuralları
```

### 4.7 Veri Modeli (frontend `types.ts` önerisi)

```ts
export type QueryMode = 'sql' | 'prometheus';

export interface MyQuery extends DataQuery {
  mode: QueryMode;
  // SQL modu
  sql?: string;                 // ham SQL (makro/değişkenli)
  format?: 'auto' | 'table' | 'time_series';
  timeColumn?: string;          // açıkça seçilen zaman kolonu
  // Prometheus modu
  promql?: string;
  legendFormat?: string;        // seri adı şablonu
}

export interface MyDataSourceOptions extends DataSourceJsonData {
  // Oracle
  o_connStr?: string; o_hostname?: string; o_port?: number;
  o_service?: string; o_sid?: string; o_user?: string;
  // Prometheus (opsiyonel)
  promEnabled?: boolean;
  promUrl?: string;
  promAuthType?: 'none' | 'basic' | 'token';
  promTlsSkipVerify?: boolean;
  defaultMode?: QueryMode;
  queryTimeoutSec?: number;
  maxRows?: number;
}

export interface MySecureJsonData {
  o_password?: string;
  promUser?: string;
  promPassword?: string;
  promToken?: string;
}
```

---

## 5. Teknoloji Yığını Önerisi

| Katman | Seçim | Gerekçe |
|---|---|---|
| Grafana SDK (Go) | `grafana-plugin-sdk-go` **güncel** (mevcut v0.156.0 2023 başı; son kararlıya yükselt) | Frame/label/stream/annotations API'leri ve düzeltmeler |
| Oracle sürücü | `sijms/go-ora/v2` (güncel sürüm) | Pure Go, OCI gerektirmez; `ColumnType` ve `QueryContext` destekler |
| SQL editörü | `@grafana/sql` + Monaco tabanlı editor komponenti (Postgres/MySQL eklentilerindeki desen) | Tamamlama, makro desteği, hazır UI |
| PromQL editörü | Grafana Prometheus eklentisinin editor API'si referans alınır; `@grafana/prometheus` paketleri (yalnızca editor) | PromQL ayrıştırıcı/otomatik tamamlama hazır |
| HTTP (Prometheus proxy) | `net/http` + `encoding/json` (veya `prometheus/client_golang/api`) | Hafif, yeterli |
| Test | Go `testing` + `testcontainers-go` (Oracle XE) ; frontend Jest + Playwright (smoke) | Mevcut `datasource_test.go` kalıbı genişletilir |
| Dev ortamı | docker-compose: Grafana + Oracle XE 21.3 + Prometheus + `oracle-db-appdev-monitoring` | Uçtan uca local dev |

---

## 6. Uygulama Planı (Fazlar ve Efor Tahmini)

> Efor tahminleri 1 geliştirici için person-gün (pg) cinsindendir; Go+React yetkinliği varsayılır.

| Faz | Kapsam | Çıktı | Efor |
|---|---|---|---|
| **F0 — İskelet & dev ortamı** | Repo yapısı, `plugin.json`, `datasource.Manage`, docker-compose (Grafana+Oracle+Prometheus+exporter), build/run loop, CI şablonu | Çalışan boş data source + demo ortamı | 2–3 pg |
| **F1 — Oracle tip haritalama + time-series** | `ColumnType` okuma, tip çözümleme, time field üretimi, Format-as, `$__interval`/`$__timeFilter`/`$__unixEpoch*` makroları, `context` iptali, havuz ayarları, satır limiti | Timeseries ve table panelleri Oracle'dan doğrudan çalışır; **temel alarmlar** (Katman 1, SQL) | 6–8 pg |
| **F2 — Prometheus modu** | Config bölümü (toggle), PromQL proxy (query + query_range), matrix/vector→frame, metric tarayıcı, legend format, Prometheus health check | Prometheus modu çalışır; alarm Katman 1 (PromQL) | 5–7 pg |
| **F3 — Alarm zenginleştirme** | Alerting bağlamında makro güvenliği, "rows > 0" tablo kalıbı dokümantasyonu ve örnek şablonlar, (ops.) Alertmanager metrik görünümü | Alarmlar her iki modda da sağlam | 3–4 pg |
| **F4 — Değişkenler & UX** | Çok değerli değişken `IN (…)`, query variable'lar (SQL + Prometheus label_values), otomatik tamamlama, sorgu geçmişi, birim/etiket config, annotations | Üretim kalitesi kullanım kolaylığı | 4–6 pg |
| **F5 — Dayanıklılık & yayın** | ORA hata çözümleme, TLS/Wallet, sınırlar/streaming, kendi `oracle_datasource_*` metrikleri, dokümantasyon, paketleme/sign, e2e test | Dağıtılabilir, bakımı yapılabilir ürün | 4–6 pg |

**Toplam (F0–F5): ~24–34 person-gün.** Kritik yol F1 ve F2'dir; F3–F5 paralelize edilebilir.

**F1'in tanımı önemli:** F1 tek başına "Prometheus olmadan zaman serisi + alarm" sağlar — yani kullanıcının ilk şikâyetinin (zamansal grafik yok) kök çözümü F1'de biter. F2 "isteğe bağlı Prometheus butonu"nu ekler.

---

## 7. Test Stratejisi

- **Unit (Go):** tip haritalama (`number→float64`, `date→time.Time`, null), makro genişletme, Prometheus matrix/vector→frame çevrimi (fixture JSON), değişken interpolasyonu.
- **Entegrasyon (Go + testcontainers):** dockerized Oracle XE'ye gerçek SQL; `datasource_test.go` kalıbı genişletilir; `checkHealth` senaryoları.
- **Prometheus entegrasyonu:** testcontainers Prometheus + exporter; 2–3 dakikalık scrape → `query_range` doğrulama.
- **e2e:** Playwright ile Grafana'da panel açma, mod değiştirme, alarm kuralı oluşturma (Grafana API üzerinden kural inject + durum kontrolü).
- **Manuel akışlar:** query variable, multi-value değişken, annotations, büyük sonuç/iptal davranışı.

---

## 8. Dağıtım ve Yayın

- Aynı paketleme deseni: `mage build` → `.tar.gz`, klasöre çıkart, `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=<plugin-id>` (veya **Grafana plugin sign** — topluluk/özel imza süreciyle kalıcı çözüm).
- docker-compose ile demo ortamı; README'de kurulum (Grafana + Oracle + Prometheus + exporter).
- CI: `tag_validation` + `release_creation` (mevcut GitHub Actions deseninden devam).

---

## 9. Riskler ve Dikkat Edilecekler

| Risk | Değerlendirme / Önlem |
|---|---|
| `go-ora/v2` bazı köşe tipleri (ör. XMLTYPE, INTERVAL YEAR TO MONTH, UDT) kısmen destekler | Tip haritalamasını güvenli default'larla yap; desteklenmeyen tip için okunur hata + önerilen `CAST(...)` |
| Canlı SQL her panel yenilemesinde Oracle'a gider | `$__interval`/Format-as ile veri hacmini sınırla; yüksek frekanslı senaryoda Prometheus modunu öner; `maxRows` varsayılanı (örn. 100k) koy |
| Oracle XE container lisansı (kabul sözleşmesi) | docker-compose'a lisans kabulü ve doğru image notu ekle |
| PromQL ile SQL anlambilim farkı (kullanıcı kafa karışıklığı) | Mod değişince örnek sorgular ve yardım metinleri göster |
| Grafana SDK sürüm kırılmaları | SDK'yı sabitle, CI'da e2e ile dogfood |
| Alerting bağlamında dashboard değişkeni eksikliği | Makro motoru varsayılan değerlere düşer + hata mesajı; testle doğrula |
| Multi-instance / TNS / Wallet karmaşıklığı | Faz 5'e planla; önce tek bağlantı deseni |
| Alarm gürültüsü | Eşik/again interval, oranlar için örnek şablon alarm paketleri hazırla |

---

## 10. Özellik Karşılaştırma Tablosu (Mevcut → Yeni)

| Özellik | Mevcut (`albertowd/oracle-grafana`) | Yeni (öneri) |
|---|---|---|
| Oracle bağlantısı | ✅ host/service/sid/connString | ✅ + TLS/Wallet, havuz, timeout |
| Sorgu tipi | SELECT (string dizi) | SELECT, **tipli** (sayı/zaman/metin) |
| Time-series | ❌ (string "zaman") | ✅ otomatik + seçilebilir zaman kolonu |
| Tablo panel | ⚠️ (null="(null)") | ✅ (gerçek null, tip korunur) |
| Prometheus | ❌ | ✅ opsiyonel mod (buton/sekme) |
| Alarm | ❌ | ✅ Grafana Unified Alerting (SQL + PromQL) |
| Standart makrolar | `$__from`, `$__to` | + `$__time`, `$__timeFilter`, `$__interval`, `$__unixEpoch*` |
| Çok değerli değişken | ❌ ("future") | ✅ `IN (…)` |
| Annotations | ❌ | ✅ |
| Sorgu iptali | ❌ (ctx yok sayılır) | ✅ |
| Otomatik tamamlama | ❌ | ✅ (Oracle metadata) |
| Query variable | ✅ tek sütun | ✅ + Prometheus `label_values` |
| Metrik self-monitoring | plugin.json `metrics:true` (ham) | ✅ `oracle_datasource_*` |
| Hata mesajları | ham ORA | ✅ çözümlenmiş |
| Lisans | Apache-2.0 | Apache-2.0 (önerilen) |

---

## 11. Sonraki Adımlar (bu rapor sonrası)

1. **F0 onayı:** İskelet + docker-compose dev ortamı ile "çalışan boş data source" kurulur; ekibin geri kalanı mimariyi somut görür.
2. **F1 kilidi:** Tip haritalama + time-series + temel alarm (Prometheus'suz) — kullanıcının birincil şikâyetini çözer.
3. **F2:** Prometheus modu + resmi exporter entegrasyon rehberi.
4. Karar noktaları: (a) Prometheus modunun backend-proxy mi yoksa frontend'den mi iletileceği — **öneri backend-proxy** (sırlar tarayıcıya inmez, CORS sorunu olmaz); (b) eksik kalan herhangi bir alan için raporun revizyonu.

> **Not:** Bu rapor, açık kaynak kod incelemesi (Apache-2.0, satır-satır doğrulanmış) ve resmi Oracle exporter dokümantasyonuna dayanır; kod kopyalansa bile lisans koşullarına uyulması önerilir.
