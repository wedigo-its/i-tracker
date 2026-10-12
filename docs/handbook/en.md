title: i-Tracker documentation
lead: How to install i-Tracker, measure your internet connection regularly and see which route your data takes. At the end you will find all settings to look up.
version: Version 2.0

## overview | Overview

i-Tracker regularly measures how fast your internet connection really is, and keeps the results. That way you do not just see a single value, but whether performance drops at certain times of day or towards a certain destination.

Unlike a speed test in the browser, i-Tracker measures against test servers of your choice, for example at your data center, in a branch office or at a public provider. It uses the proven measuring tool **iperf3** for this. In addition, it shows on a map which stations your data passes on the way to the destination.

i-Tracker runs on one of your own computers and is used in the browser. It is free and open source.

![The dashboard with the results per server](i-tracker-dashboard.webp)

## requirements | Requirements

- **A computer** running Windows that is always on, for example a small server or a VM. With Docker, Linux works too, see [Installation](#install).
- **iperf3 version 3.17 or later.** i-Tracker does not include it but can install it at the push of a button (see [Setup](#setup)).
- **At least one test server** running iperf3 as a server (`iperf3 -s`). This can be a computer of your own, or you pick a public server from the list in i-Tracker.
- **Network connections:**
  - from the i-Tracker computer to the test server on its iperf3 port (usually 5201)
  - for route tracing: traceroute packets to the internet (sometimes blocked in company networks)
  - for the map: once a day a connection to db-ip.com to update the location data
  - from your workstation to the interface on port 8000

## install | Installation

Put the program file into a folder of its own, for example `C:\Program Files\i-Tracker`. i-Tracker creates its settings and measurement data in this folder.

The first time, start i-Tracker by hand. This creates the settings file and the initial password.

```windows
cd "C:\Program Files\i-Tracker"
.\i-tracker.exe
```

Then open the interface in the browser at `http://localhost:8000`, from other computers at `http://<computer name>:8000`. Allow port 8000 in the firewall for this.

### Run as a service

To keep i-Tracker measuring permanently, also after a restart, set it up as a Windows service. This needs administrator rights.

```windows
.\i-tracker.exe service install
.\i-tracker.exe service start
```

### With Docker

For Linux, the source code contains a Docker template (`Dockerfile`, `docker-compose.yml`). The image already contains iperf3 and traceroute. All data lives in the volume `/data`. This option is still new and less tested than the installation on Windows.

```docker
docker compose up -d
```

## setup | Setup

**Log in:** On the first start, i-Tracker creates the user `admin` with a random initial password. It is shown once in the console window. If i-Tracker already runs as a service, you find it in the file `initial-admin-password.txt` in the program folder. After the first login, choose your own password (at least 10 characters). Delete the file afterwards.

**Install iperf3:** If iperf3 is missing, the status dot at the top right shows a hint. As an administrator you can have it installed there with one click. On Windows this uses winget. A restart of the service may be needed afterwards.

**Add the first test server:** Under **Servers** you set what to measure against, see [Test servers](#servers).

## servers | Test servers

A test server is a computer on which iperf3 runs as the counterpart. Under **Servers** you create a profile per destination:

- **Name and address** (name or IP address) and the port, usually 5201
- **Test defaults:** protocol, direction, duration and number of parallel connections for the regular measurements
- **Test regularly:** every how many minutes to measure automatically (default: 30)
- **Auto trace:** after each measurement, also record the route to the destination
- **Server active:** switches the profile off temporarily without deleting it

Instead of entering an address yourself, you can take a **public server** from the built-in list. Public servers are often busy. Do not measure there too often, and use few parallel connections.

![Managing test servers](i-tracker-servers.webp)

> Only one measurement runs at a time so measurements do not distort each other. Others wait until it is their turn. With many servers and short intervals this can cause delays.

## tests | Measurements

Under **Tests** you start a measurement by hand and see all previous results.

- **Protocol:** TCP measures throughput as file transfers experience it. UDP also measures fluctuations (jitter) and lost packets, which matters for telephony and video.
- **Direction:** download (from the server to you), upload (from you to the server) or both at once.
- **Duration:** 1 to 300 seconds. 10 to 30 seconds are usually enough.
- **Parallel connections:** several connections at once make better use of fast lines.
- **UDP bandwidth:** how much is sent with UDP. Without a value only 1 Mbit/s.

During a measurement you see the values live. A click on a measurement shows all details.

![Measurements and history](i-tracker-tests.webp)

## dashboard | Dashboard

The dashboard sums everything up: how many servers and measurements there are, the averages for download and upload, and the history per server over one hour, one day, one week or one month.

With **Min. download** and **Min. upload** you set from which value a measurement counts as too slow. These thresholds only apply in your browser and do not send any notification.

## peering | Route tracing and map

Under **Peering map** you see which stations your data passes on its way to a test server and roughly where these stations are. That way you spot, for example, detours through another country that make a connection slow.

- Choose a server and click **Start traceroute**. The stations appear one after the other.
- The table shows address, name, response time and location for each station.
- Locations that cannot be determined are estimated by i-Tracker from the neighbouring stations. They are marked as "estimated".
- You can open saved routes again later.

![Route to the destination on the map](i-tracker-peering.webp)

> Route tracing works with IPv4 addresses. Some routers on the internet do not answer; then single stations are missing. That is normal.

## geoip | Location data

For the map, i-Tracker needs a database that assigns IP addresses to places. It downloads the free database from DB-IP by itself and keeps it up to date once a day.

If you have an account with MaxMind, you can use their database instead. Enter account ID and license key (`geoip.maxmind_account_id`, `geoip.maxmind_license_key`). Without internet access, switch off the update with `geoip.auto_update: false` and put the file in place yourself.

## guest | Guest view

Without logging in, anyone who opens the interface sees the results but cannot change anything or start a measurement. "Read only" is shown at the top. This is handy for a screen in the office or for colleagues who only want to have a look.

If nothing should be visible without logging in, set `auth.public_read` to `false`.

![The guest view](i-tracker-guest.webp)

## users | Users

Under **Admin**, administrators create and delete further users.

- **Administrators** may do everything, including managing users, deleting old measurements and installing iperf3.
- **Regular users** may maintain servers and start measurements.

The **Admin** area is also where you clean up: delete measurements and routes older than a certain number of days. i-Tracker never deletes anything by itself.

## updates | Installing a new version

i-Tracker does not update itself. For a new version, stop the service, replace the program file and start the service again. Your data is kept; the database is adjusted automatically on start if needed.

```windows
.\i-tracker.exe service stop
.\i-tracker.exe service start
```

```docker
docker compose up -d --build
```

## backup | Backup

Back up the program folder (with Docker, the volume `/data`). These matter:

- `config.yaml`: your settings
- the folder `data`: all users, servers, measurements and routes. Safest to copy while the service is stopped.

i-Tracker downloads the location data in the folder `geoip` again when needed.

## troubleshooting | Troubleshooting

**"iperf3 not found":** Install iperf3 via the status dot or by hand, or enter the path in `iperf.path`. Restart the service after an installation.

**Measurements fail:** Does `iperf3 -s` run on the test server, and can its port be reached? Public servers are often busy; then try again later or use another server.

**The interface cannot be reached from other computers:** Allow port 8000 in the firewall and check that `web.listen` is `0.0.0.0:8000`.

**Stations or places are missing on the map:** Some routers do not answer traceroute, and not every address can be assigned to a place. That is normal.

**Forgot the password:** Another administrator can create a new user. If there is none, write to us using the [contact form](/contact).

## reference | Look up

This part is for looking things up when you want to change a particular setting.

### Settings

The settings are in the file `config.yaml` in the program folder. Open it with a text editor and restart the service after a change. Instead, you can also set each value with the environment variable shown below its name in the table.

{{config}}

- web.listen :: Address at which the interface can be reached. The file created on the first start sets `0.0.0.0:8000`, so it is open to all computers in the network.
- storage.path :: File with all measurements, servers and users.
- auth.secret_key :: Secret key for logins. Created randomly on the first start. Do not share it. Changing it logs out all users.
- auth.session_ttl :: After how long without activity you have to log in again.
- auth.public_read :: Guest view: show results without logging in.
- scheduler.enabled :: Switch regular measurements on or off.
- iperf.path :: Where iperf3 is. Empty: i-Tracker searches itself.
- iperf.auto_install :: Install iperf3 on start without asking if it is missing.
- geoip.path :: File with the location data.
- geoip.auto_update :: Update the location data daily.
- geoip.maxmind_account_id :: Account ID at MaxMind, only together with the license key.
- geoip.maxmind_license_key :: License key at MaxMind.
- map.tile_url :: Your own map service for a more detailed map, for example `https://tile.example.org/{z}/{x}/{y}.png`. Empty: the built-in world map.
- map.tile_attribution :: Source notice for your own map service.
- log.level :: How detailed the log is: `debug`, `info`, `warn` or `error`.
- log.format :: Layout of the log: `text` for reading, `json` for analysis tools.

### Commands

Enter these commands in a console window in the program folder.

{{cli}}

- -config :: Use a different settings file.
- -no-tui :: Has no effect; only there so calls work as with the other wedigo programs.
- -version :: Show the version number.
- service install :: Set up as a service (with administrator rights).
- service uninstall :: Remove the service again.
- service start :: Start the service.
- service stop :: Stop the service.
- service restart :: Restart the service.
- service status :: Show whether the service is running.

### Interface for your own analyses

You can also fetch all data of the interface as JSON, for example for your own scripts or monitoring systems. Reading addresses can be reached without logging in while the guest view is on. For everything else, log in with `POST /api/auth/login` and send the value `X-CSRF-Token` with changing requests.

{{api}}

- GET /api/info :: Name and version.
- GET /health :: Answers as long as i-Tracker is running. Useful for monitoring systems.
- GET /api/status :: State: version, whether iperf3 is available or being installed, whether regular measurements run, and which location data is loaded.
- POST /api/auth/login :: Log in with user and password.
- POST /api/auth/logout :: Log out.
- POST /api/auth/logout-all :: Log out on all devices.
- GET /api/auth/status :: Whether the guest view is on and whether the initial password still has to be changed.
- GET /api/auth/me :: Who is logged in.
- POST /api/auth/change-password :: Change the password.
- POST /api/auth/register :: Create a new user.
- GET /api/auth/users :: All users.
- DELETE /api/auth/users/{user_id} :: Delete a user.
- GET /api/servers :: All test servers.
- POST /api/servers :: Create a test server.
- GET /api/servers/{server_id} :: One test server.
- PUT /api/servers/{server_id} :: Change a test server.
- DELETE /api/servers/{server_id} :: Delete a test server with all its measurements.
- GET /api/tests :: Measurements, filterable by server, status and period.
- POST /api/tests/run :: Start a measurement.
- GET /api/tests/{test_id} :: One measurement with all values.
- DELETE /api/tests/{test_id} :: Delete a measurement.
- GET /api/tests/{test_id}/live :: Interim state of a running measurement.
- GET /api/tests/live/stream :: Running measurements as a continuous data stream.
- GET /api/tests/server/{server_id}/latest :: Latest measurement of a server.
- GET /api/stats/dashboard :: Figures of the dashboard.
- GET /api/stats/servers :: Figures of all servers.
- GET /api/stats/servers/{server_id} :: Figures of one server.
- POST /api/tests/{test_id}/trace :: Start a route trace for a measurement.
- GET /api/tests/{test_id}/trace :: Route trace of a measurement.
- DELETE /api/tests/{test_id}/trace :: Delete the route trace of a measurement.
- POST /api/traces :: Record a route trace to any destination.
- GET /api/traces :: Saved route traces.
- GET /api/traces/{trace_id} :: One route trace with all stations.
- GET /api/traces/test/{test_id} :: Route traces for a measurement.
- DELETE /api/traces/{trace_id} :: Delete a route trace.
- GET /api/live-trace/stream/{destination} :: Route trace live, station by station.
- DELETE /api/admin/cleanup/tests :: Delete old measurements, `?days=…`.
- DELETE /api/admin/cleanup/traces :: Delete old route traces, `?days=…`.
- GET /api/admin/stats/database :: Size and content of the database.
- POST /api/iperf3/install :: Install iperf3.
- GET /api/public-servers :: List of public test servers.
- GET /api/public-servers/search :: Search the list of public test servers, `?query=…`.
