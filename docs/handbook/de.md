title: i-Tracker-Dokumentation
lead: So installieren Sie i-Tracker, messen Ihre Internetverbindung regelmäßig und sehen, über welchen Weg Ihre Daten laufen. Am Ende finden Sie alle Einstellungen zum Nachschlagen.
version: Version 0.x

## overview | Überblick

i-Tracker misst regelmäßig, wie schnell Ihre Internetverbindung wirklich ist, und hebt die Ergebnisse auf. So sehen Sie nicht nur einen einzelnen Wert, sondern ob die Leistung zu bestimmten Tageszeiten einbricht oder zu einem bestimmten Ziel schlechter ist.

Anders als ein Speedtest im Browser misst i-Tracker gegen Messserver Ihrer Wahl, zum Beispiel bei Ihrem Rechenzentrum, in einer Außenstelle oder bei einem öffentlichen Anbieter. Dafür nutzt es das bewährte Messprogramm **iperf3**. Zusätzlich zeigt es auf einer Karte, über welche Stationen Ihre Daten zum Ziel laufen.

i-Tracker läuft auf einem Ihrer eigenen Rechner und wird im Browser bedient. Es ist kostenlos und quelloffen.

![Das Dashboard mit den Messwerten je Server](i-tracker-dashboard.webp)

## requirements | Voraussetzungen

- **Ein Rechner** mit Windows, der dauerhaft läuft, zum Beispiel ein kleiner Server oder eine VM. Mit Docker geht auch Linux, siehe [Installation](#install).
- **iperf3 ab Version 3.17.** i-Tracker bringt es nicht selbst mit, kann es aber auf Knopfdruck installieren (siehe [Einrichtung](#setup)).
- **Mindestens ein Messserver**, auf dem iperf3 als Server läuft (`iperf3 -s`). Das kann ein eigener Rechner sein, oder Sie wählen einen öffentlichen Server aus der Liste in i-Tracker.
- **Netzwerkverbindungen:**
  - vom i-Tracker-Rechner zum Messserver auf dessen iperf3-Port (meist 5201)
  - für die Wegverfolgung: Traceroute-Pakete ins Internet (in Firmennetzen manchmal gesperrt)
  - für die Karte: einmal am Tag eine Verbindung zu db-ip.com, um die Standortdaten zu aktualisieren
  - für [Updates](#updates): alle 6 Stunden HTTPS zu www.wedigo.net
  - von Ihrem Arbeitsplatz zur Oberfläche auf Port 8000

## install | Installation

Legen Sie die Programmdatei in einen eigenen Ordner, zum Beispiel `C:\Program Files\i-Tracker`. Einstellungen und Messdaten legt i-Tracker in diesem Ordner an.

Starten Sie i-Tracker beim ersten Mal von Hand. Dabei entstehen die Einstellungsdatei und das Startpasswort.

```windows
cd "C:\Program Files\i-Tracker"
.\i-tracker.exe
```

Danach öffnen Sie die Oberfläche im Browser unter `http://localhost:8000`, von anderen Rechnern unter `http://<rechnername>:8000`. Geben Sie dafür Port 8000 in der Firewall frei.

### Als Dienst einrichten

Damit i-Tracker dauerhaft misst, auch nach einem Neustart, richten Sie es als Windows-Dienst ein. Dafür brauchen Sie Administratorrechte.

```windows
.\i-tracker.exe service install
.\i-tracker.exe service start
```

### Mit Docker

Für Linux gibt es eine Docker-Vorlage im Quellcode (`Dockerfile`, `docker-compose.yml`). Das Abbild enthält iperf3 und Traceroute bereits. Alle Daten liegen im Volume `/data`. Diese Variante ist noch neu und weniger erprobt als die Installation unter Windows.

```docker
docker compose up -d
```

## setup | Einrichtung

**Anmelden:** Beim ersten Start legt i-Tracker den Benutzer `admin` mit einem zufälligen Startpasswort an. Es steht einmalig im Konsolenfenster. Läuft i-Tracker schon als Dienst, finden Sie es in der Datei `initial-admin-password.txt` im Programmordner. Nach der ersten Anmeldung vergeben Sie ein eigenes Passwort (mindestens 8 Zeichen). Löschen Sie die Datei danach.

**iperf3 installieren:** Fehlt iperf3, zeigt der Statuspunkt oben rechts einen Hinweis. Als Administrator können Sie es dort mit einem Klick installieren lassen. Unter Windows geschieht das über winget. Danach kann ein Neustart des Dienstes nötig sein.

**Ersten Messserver anlegen:** Unter **Server** legen Sie fest, wogegen gemessen wird, siehe [Messserver](#servers).

## servers | Messserver

Ein Messserver ist ein Rechner, auf dem iperf3 als Gegenstelle läuft. Unter **Server** legen Sie je Ziel ein Profil an:

- **Name und Adresse** (Name oder IP-Adresse) und der Port, meist 5201
- **Testvorgaben:** Protokoll, Richtung, Dauer und Zahl der parallelen Verbindungen für die regelmäßigen Messungen
- **Regelmäßig testen:** alle wie viele Minuten automatisch gemessen wird (Vorgabe: 30)
- **Auto-Trace:** nach jeder Messung zusätzlich den Weg zum Ziel aufzeichnen
- **Server aktiv:** schaltet das Profil vorübergehend ab, ohne es zu löschen

Statt eine Adresse selbst einzugeben, können Sie einen **öffentlichen Server** aus der eingebauten Liste übernehmen. Öffentliche Server sind oft stark belastet. Messen Sie dort nicht zu häufig und mit wenigen parallelen Verbindungen.

![Messserver verwalten](i-tracker-servers.webp)

> Es läuft immer nur eine Messung gleichzeitig, damit sich die Messungen nicht gegenseitig verfälschen. Weitere warten, bis sie an der Reihe sind. Bei vielen Servern mit kurzen Abständen kann es deshalb zu Verzögerungen kommen.

## tests | Messungen

Unter **Tests** starten Sie eine Messung von Hand und sehen alle bisherigen Ergebnisse.

- **Protokoll:** TCP misst den Durchsatz, wie ihn Dateiübertragungen erleben. UDP misst zusätzlich Schwankungen (Jitter) und verlorene Pakete, wichtig für Telefonie und Video.
- **Richtung:** Download (vom Server zu Ihnen), Upload (von Ihnen zum Server) oder beides gleichzeitig.
- **Dauer:** 1 bis 300 Sekunden. 10 bis 30 Sekunden sind meist genug.
- **Parallele Verbindungen:** Mehrere Verbindungen gleichzeitig lasten schnelle Leitungen besser aus.
- **UDP-Bandbreite:** wie viel bei UDP gesendet wird. Ohne Angabe nur 1 Mbit/s.

Während der Messung sehen Sie die Werte live. Ein Klick auf eine Messung zeigt alle Details.

![Messungen und Verlauf](i-tracker-tests.webp)

## dashboard | Dashboard

Das Dashboard fasst alles zusammen: wie viele Server und Messungen es gibt, die Durchschnittswerte für Download und Upload und den Verlauf je Server über eine Stunde, einen Tag, eine Woche oder einen Monat.

Mit **Min. Download** und **Min. Upload** legen Sie fest, ab wann eine Messung als zu langsam zählt. Diese Schwellwerte gelten nur in Ihrem Browser und lösen keine Benachrichtigung aus.

## peering | Wegverfolgung und Karte

Unter **Peering-Map** sehen Sie, über welche Stationen Ihre Daten zu einem Messserver laufen und wo diese Stationen ungefähr stehen. So erkennen Sie zum Beispiel Umwege über ein anderes Land, die eine Verbindung langsam machen.

- Wählen Sie einen Server und klicken Sie auf **Traceroute starten**. Die Stationen erscheinen nacheinander.
- Die Tabelle zeigt je Station Adresse, Name, Antwortzeit und Standort.
- Standorte, die sich nicht bestimmen lassen, schätzt i-Tracker aus den Nachbarstationen. Sie sind als „geschätzt“ markiert.
- Gespeicherte Wege können Sie später wieder aufrufen.

![Weg zum Ziel auf der Karte](i-tracker-peering.webp)

> Die Wegverfolgung funktioniert mit IPv4-Adressen. Manche Router im Internet antworten nicht; dann fehlen einzelne Stationen. Das ist normal.

## geoip | Standortdaten

Für die Karte braucht i-Tracker eine Datenbank, die IP-Adressen einem Ort zuordnet. Es lädt dafür von selbst die kostenlose Datenbank von DB-IP herunter und hält sie einmal am Tag aktuell.

Haben Sie ein Konto bei MaxMind, können Sie stattdessen deren Datenbank verwenden. Tragen Sie dazu Konto-ID und Lizenzschlüssel ein (`geoip.maxmind_account_id`, `geoip.maxmind_license_key`). Ohne Internetzugang schalten Sie die Aktualisierung mit `geoip.auto_update: false` ab und legen die Datei selbst ab.

## guest | Gastansicht

Ohne Anmeldung sieht jeder, der die Oberfläche öffnet, die Messergebnisse, kann aber nichts ändern und keine Messung starten. Oben steht dann „Nur Lesen“. Das ist praktisch für einen Bildschirm im Büro oder für Kollegen, die nur nachsehen möchten.

Soll ohne Anmeldung gar nichts zu sehen sein, setzen Sie `auth.public_read` auf `false`.

![Die Gastansicht](i-tracker-guest.webp)

## users | Benutzer

Unter **Admin** legen Administratoren weitere Benutzer an und löschen sie.

- **Administratoren** dürfen alles, auch Benutzer verwalten, alte Messungen löschen, iperf3 installieren und Updates einspielen.
- **Normale Benutzer** dürfen Server pflegen und Messungen starten.

Im Bereich **Admin** räumen Sie auch auf: Messungen und Wegverfolgungen älter als eine bestimmte Zahl von Tagen löschen. Von selbst löscht i-Tracker nichts.

## updates | Updates

i-Tracker hält sich selbst aktuell. Es schaut regelmäßig auf www.wedigo.net nach, ob es eine neue Version gibt, prüft, dass sie wirklich von uns stammt, und installiert sie. Ihre Daten bleiben erhalten; die Datenbank wird beim Start bei Bedarf automatisch angepasst.

Wie das abläuft, legen Sie in der Einstellung `update.mode` fest:

- `auto` (Standard): Neue Versionen werden installiert, der Dienst startet danach kurz neu.
- `notify`: i-Tracker meldet eine neue Version nur. Sie installieren sie selbst in der Oberfläche.
- `off`: Keine Prüfung auf Updates.

Mit `update.window` legen Sie eine Uhrzeit fest, zu der installiert werden darf, zum Beispiel `"02:00-05:00"`. Den Stand sehen Administratoren unter **Admin → Software-Update** und können dort auch von Hand prüfen und installieren.

> Automatisch installiert wird nur, wenn i-Tracker als Dienst läuft und in seinen Programmordner schreiben darf. Starten Sie es von Hand im Konsolenfenster, meldet es neue Versionen nur; nach einer Installation von Hand starten Sie es neu.

Alle Versionen kommen aus derselben Quelle, auch der Wechsel auf eine neue Hauptversion (etwa von 0.x auf 1.0) ist ein gewöhnliches Update.

Mit Docker aktualisiert sich i-Tracker nicht selbst; dort bauen Sie das Image neu:

```docker
docker compose up -d --build
```

### Zurück zur vorigen Version

i-Tracker hebt die vorige Version auf. Startet eine neue Version nicht richtig, kehrt es von selbst zur alten zurück. Sie können das auch von Hand tun:

```windows
.\i-tracker.exe rollback
.\i-tracker.exe service restart
```

```linux
sudo ./i-tracker rollback
sudo ./i-tracker service restart
```

## backup | Sicherung

Sichern Sie den Programmordner (bei Docker das Volume `/data`). Wichtig sind:

- `config.yaml`: Ihre Einstellungen
- der Ordner `data`: alle Benutzer, Server, Messungen und Wege. Am sichersten kopieren Sie ihn bei gestopptem Dienst.

Die Standortdaten im Ordner `geoip` lädt i-Tracker bei Bedarf neu herunter.

## troubleshooting | Fehlerbehebung

**„iperf3 nicht gefunden“:** Installieren Sie iperf3 über den Statuspunkt oder von Hand, oder tragen Sie den Pfad in `iperf.path` ein. Nach einer Installation den Dienst neu starten.

**Messungen schlagen fehl:** Läuft auf dem Messserver `iperf3 -s`, und ist dessen Port erreichbar? Öffentliche Server sind oft belegt; dann hilft ein späterer Versuch oder ein anderer Server.

**Die Oberfläche ist von anderen Rechnern nicht erreichbar:** Port 8000 in der Firewall freigeben und prüfen, dass `web.listen` auf `0.0.0.0:8000` steht.

**Auf der Karte fehlen Stationen oder Orte:** Manche Router antworten nicht auf Traceroute, und nicht jede Adresse lässt sich einem Ort zuordnen. Das ist normal.

**Passwort vergessen:** Ein anderer Administrator kann einen neuen Benutzer anlegen. Gibt es keinen, schreiben Sie uns über das [Kontaktformular](/contact).

## reference | Nachschlagen

Dieser Teil ist zum Nachschlagen gedacht, wenn Sie etwas Bestimmtes einstellen möchten.

### Einstellungen

Die Einstellungen stehen in der Datei `config.yaml` im Programmordner. Öffnen Sie sie mit einem Texteditor und starten Sie den Dienst nach einer Änderung neu. Jeden Wert können Sie stattdessen auch über die Umgebungsvariable setzen, die in der Tabelle unter dem Namen steht.

{{config}}

- web.listen :: Unter welcher Adresse die Oberfläche erreichbar ist. Die beim ersten Start angelegte Datei setzt `0.0.0.0:8000`, also für alle Rechner im Netz.
- storage.path :: Datei mit allen Messdaten, Servern und Benutzern.
- auth.secret_key :: Geheimer Schlüssel für die Anmeldung. Wird beim ersten Start zufällig erzeugt. Nicht weitergeben. Ändern meldet alle Benutzer ab.
- auth.session_ttl :: Nach welcher Zeit ohne Aktivität Sie sich neu anmelden müssen.
- auth.public_read :: Gastansicht: Ergebnisse auch ohne Anmeldung zeigen.
- scheduler.enabled :: Regelmäßige Messungen ein- oder ausschalten.
- iperf.path :: Wo iperf3 liegt. Leer: i-Tracker sucht selbst.
- iperf.auto_install :: iperf3 beim Start ohne Rückfrage installieren, falls es fehlt.
- geoip.path :: Datei mit den Standortdaten.
- geoip.auto_update :: Standortdaten täglich aktualisieren.
- geoip.maxmind_account_id :: Konto-ID bei MaxMind, nur zusammen mit dem Lizenzschlüssel.
- geoip.maxmind_license_key :: Lizenzschlüssel bei MaxMind.
- map.tile_url :: Eigener Kartendienst für eine detailliertere Karte, zum Beispiel `https://tile.example.org/{z}/{x}/{y}.png`. Leer: die eingebaute Weltkarte.
- map.tile_attribution :: Quellenhinweis für den eigenen Kartendienst.
- update.mode :: `auto`, `notify` oder `off`, siehe [Updates](#updates).
- update.interval :: Wie oft nach Updates geschaut wird.
- update.window :: Uhrzeit, zu der Updates installiert werden dürfen, zum Beispiel `"02:00-05:00"`. Leer: jederzeit.
- log.level :: Wie ausführlich das Protokoll ist: `debug`, `info`, `warn` oder `error`.
- log.format :: Aufbau des Protokolls: `text` zum Lesen, `json` für Auswertungsprogramme.

### Befehle

Diese Befehle geben Sie im Konsolenfenster im Programmordner ein.

{{cli}}

- -config :: Eine andere Einstellungsdatei verwenden.
- -no-tui :: Ohne Wirkung; nur vorhanden, damit Aufrufe wie bei den anderen wedigo-Programmen funktionieren.
- -version :: Die Versionsnummer anzeigen.
- service install :: Als Dienst einrichten (mit Administratorrechten).
- service uninstall :: Den Dienst wieder entfernen.
- service start :: Den Dienst starten.
- service stop :: Den Dienst stoppen.
- service restart :: Den Dienst neu starten.
- service status :: Anzeigen, ob der Dienst läuft.
- rollback :: Zur Version vor dem letzten Update zurückkehren. Danach den Dienst neu starten.

### Schnittstelle für eigene Auswertungen

Alle Daten der Oberfläche können Sie auch als JSON abrufen, etwa für eigene Skripte oder Überwachungssysteme. Lesende Adressen sind mit eingeschalteter Gastansicht auch ohne Anmeldung erreichbar. Für alles andere melden Sie sich mit `POST /api/auth/login` an und schicken bei ändernden Anfragen den Wert `X-CSRF-Token` mit.

{{api}}

- GET /api/info :: Name und Version.
- GET /health :: Antwortet, solange i-Tracker läuft. Gut für Überwachungssysteme.
- GET /api/status :: Zustand: Version, ob iperf3 vorhanden ist oder gerade installiert wird, ob regelmäßige Messungen laufen und welche Standortdaten geladen sind.
- POST /api/auth/login :: Anmelden mit Benutzer und Passwort.
- POST /api/auth/logout :: Abmelden.
- POST /api/auth/logout-all :: Auf allen Geräten abmelden.
- GET /api/auth/status :: Ob die Gastansicht aktiv ist und ob noch das Startpasswort geändert werden muss.
- GET /api/auth/me :: Wer gerade angemeldet ist.
- POST /api/auth/change-password :: Passwort ändern.
- POST /api/auth/register :: Neuen Benutzer anlegen.
- GET /api/auth/users :: Alle Benutzer.
- DELETE /api/auth/users/{user_id} :: Benutzer löschen.
- GET /api/servers :: Alle Messserver.
- POST /api/servers :: Messserver anlegen.
- GET /api/servers/{server_id} :: Ein Messserver.
- PUT /api/servers/{server_id} :: Messserver ändern.
- DELETE /api/servers/{server_id} :: Messserver mit allen seinen Messungen löschen.
- GET /api/tests :: Messungen, filterbar nach Server, Status und Zeitraum.
- POST /api/tests/run :: Messung starten.
- GET /api/tests/{test_id} :: Eine Messung mit allen Werten.
- DELETE /api/tests/{test_id} :: Messung löschen.
- GET /api/tests/{test_id}/live :: Zwischenstand einer laufenden Messung.
- GET /api/tests/live/stream :: Laufende Messungen als fortlaufender Datenstrom.
- GET /api/tests/server/{server_id}/latest :: Letzte Messung eines Servers.
- GET /api/stats/dashboard :: Kennzahlen des Dashboards.
- GET /api/stats/servers :: Kennzahlen aller Server.
- GET /api/stats/servers/{server_id} :: Kennzahlen eines Servers.
- POST /api/tests/{test_id}/trace :: Wegverfolgung zu einer Messung starten.
- GET /api/tests/{test_id}/trace :: Wegverfolgung einer Messung.
- DELETE /api/tests/{test_id}/trace :: Wegverfolgung einer Messung löschen.
- POST /api/traces :: Wegverfolgung zu einem beliebigen Ziel aufzeichnen.
- GET /api/traces :: Gespeicherte Wegverfolgungen.
- GET /api/traces/{trace_id} :: Eine Wegverfolgung mit allen Stationen.
- GET /api/traces/test/{test_id} :: Wegverfolgungen zu einer Messung.
- DELETE /api/traces/{trace_id} :: Wegverfolgung löschen.
- GET /api/live-trace/stream/{destination} :: Wegverfolgung live, Station für Station.
- DELETE /api/admin/cleanup/tests :: Alte Messungen löschen, `?days=…`.
- DELETE /api/admin/cleanup/traces :: Alte Wegverfolgungen löschen, `?days=…`.
- GET /api/admin/stats/database :: Größe und Inhalt der Datenbank.
- POST /api/iperf3/install :: iperf3 installieren.
- GET /api/update :: Stand der Updates: Modus, laufende und neueste Version, letzte Prüfung und warum gerade nicht automatisch installiert wird (`blocked`).
- POST /api/update/check :: Jetzt nach Updates schauen.
- POST /api/update/install :: Ein verfügbares Update installieren.
- GET /api/public-servers :: Liste öffentlicher Messserver.
- GET /api/public-servers/search :: In der Liste öffentlicher Messserver suchen, `?query=…`.
