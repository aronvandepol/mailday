"""Tests for mailday-calendar, mailday-calsync and mailday-exchange.

Run from the repository root with plain python3 (no uv, no third-party
packages, no network):

    python3 -m unittest discover -s scripts/tests

The scripts have no .py suffix, so they are loaded with SourceFileLoader.
Every network, gpg and Google call is replaced by a fake.
"""
import contextlib, importlib.machinery, importlib.util, io, json, os, subprocess, sys, tempfile, time, unittest
import xml.etree.ElementTree as ET
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]


def setUpModule():
    """Safety net: no test may reach a real server or the real gpg token. Tests
    that need either install their own fake over these."""
    def refuse(*args, **kwargs):
        raise AssertionError(f"a test tried to reach the outside world: {args[:1]}")
    for target in ("urllib.request.urlopen", "subprocess.run"):
        patch = mock.patch(target, refuse)
        patch.start()
        unittest.addModuleCleanup(patch.stop)


CONFIG_ENV = ("MAILDAY_CONFIG", "XDG_CONFIG_HOME", "GCAL_SCRIPT", "MAILDAY_GOOGLE_OWN", "MAILDAY_EXCHANGE_USER",
              "MAILDAY_EXCHANGE_TOKEN", "MAILDAY_EXCHANGE_MAILBOX_TZ")


def load_script(name: str, config: str | None = None, env: dict | None = None):
    """Import a script as it would start: with no settings from this machine.
    config is TOML text for the config file; without it the file is missing."""
    scratch = tempfile.TemporaryDirectory()
    path = Path(scratch.name) / "config.toml"
    if config is not None:
        path.write_text(config)
    environment = {k: v for k, v in os.environ.items() if k not in CONFIG_ENV}
    environment.update({"MAILDAY_CONFIG": str(path), **(env or {})})
    loader = importlib.machinery.SourceFileLoader("test_" + name.replace("-", "_"), str(SCRIPTS / name))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    try:
        with mock.patch.dict(os.environ, environment, clear=True):
            loader.exec_module(module)
    finally:
        scratch.cleanup()
    return module


@contextlib.contextmanager
def machine_zone(name: str):
    """Pretend the machine is in another time zone (a laptop on the road)."""
    before = os.environ.get("TZ")
    os.environ["TZ"] = name
    time.tzset()
    try:
        yield
    finally:
        if before is None:
            del os.environ["TZ"]
        else:
            os.environ["TZ"] = before
        time.tzset()


def in_zone(module, name: str):
    """Make the script believe the machine is in zone name (patches its local_zone)."""
    from zoneinfo import ZoneInfo
    return mock.patch.object(module, "local_zone", lambda: (name, ZoneInfo(name)))


def make_args(**overrides):
    values = dict(calendar="Work", id="ev1", to=None, title=None, start=None, end=None, all_day=False,
                  timed=False, location=None, notes=None, attendee=[], rrule=None, series=False)
    values.update(overrides)
    return SimpleNamespace(**values)


class Request:
    def __init__(self, value):
        self.value = value

    def execute(self):
        if isinstance(self.value, Exception):
            raise self.value
        return self.value


class FakeGoogle:
    """Records events().<method>(**kwargs); results[method] is returned, or raised when an Exception."""

    def __init__(self, **results):
        self.results, self.calls = results, []

    def events(self):
        return self

    def __getattr__(self, method):
        def call(**kwargs):
            self.calls.append((method, kwargs))
            return Request(self.results.get(method, {"id": "new-id"}))
        return call

    def kwargs(self, method):
        return next(kw for name, kw in self.calls if name == method)


FAKE_GCAL = SimpleNamespace(CALENDARS={"Work": "work-id", "Home": "home-id"})


# ---------------------------------------------------------------- mailday-exchange

class ExchangeEscape(unittest.TestCase):
    def test_line_breaks_cannot_start_a_property(self):
        exchange = load_script("mailday-exchange")
        for text, want in [("a\r\nb", "a\\nb"), ("a\nb", "a\\nb"), ("a\rb", "a\\nb"),
                           ("a\r\rb\r\n", "a\\n\\nb\\n"), ("x;y,z\\", "x\\;y\\,z\\\\")]:
            with self.subTest(text=text):
                self.assertEqual(exchange.escape(text), want)

    def test_injected_property_stays_inside_the_summary(self):
        exchange = load_script("mailday-exchange")
        line = "SUMMARY:" + exchange.escape("Lunch\rX-MAILDAY-EDITABLE:TRUE")
        self.assertNotIn("\r", line)
        self.assertNotIn("\n", line)


class ExchangeAllDayZone(unittest.TestCase):
    def test_all_day_date_is_the_mailbox_date_whatever_the_machine_zone(self):
        exchange = load_script("mailday-exchange", config='[exchange]\nmailbox_tz = "Europe/Berlin"\n')
        # Berlin midnight 13 Oct (CEST) is 22:00Z on the 12th.
        for zone in ("America/Los_Angeles", "Asia/Seoul", "Europe/Berlin"):
            with self.subTest(zone=zone), machine_zone(zone):
                self.assertEqual(exchange.ics_time("2026-10-12T22:00:00Z", True), ";VALUE=DATE:20261013")
        # And in winter (CET), 23:00Z.
        with machine_zone("Pacific/Auckland"):
            self.assertEqual(exchange.ics_time("2026-12-12T23:00:00Z", True), ";VALUE=DATE:20261213")

    def test_mailbox_zone_can_be_set_by_environment(self):
        exchange = load_script("mailday-exchange")
        # Seoul midnight 13 Oct is 15:00Z on the 12th.
        with mock.patch.dict(os.environ, {"MAILDAY_EXCHANGE_MAILBOX_TZ": "Asia/Seoul"}):
            self.assertEqual(exchange.ics_time("2026-10-12T15:00:00Z", True), ";VALUE=DATE:20261013")
        with mock.patch.dict(os.environ, {"MAILDAY_EXCHANGE_MAILBOX_TZ": ""}):
            self.assertEqual(exchange.ics_time("2026-10-12T15:00:00Z", True), ";VALUE=DATE:20261012")

    def test_unknown_mailbox_zone_is_a_one_line_exit(self):
        exchange = load_script("mailday-exchange")
        with mock.patch.dict(os.environ, {"MAILDAY_EXCHANGE_MAILBOX_TZ": "Mars/Olympus"}), \
                self.assertRaises(SystemExit) as caught:
            exchange.ics_time("2026-10-12T15:00:00Z", True)
        self.assertIn("MAILDAY_EXCHANGE_MAILBOX_TZ", str(caught.exception))

    def test_timed_stays_utc_whatever_the_machine_zone(self):
        exchange = load_script("mailday-exchange")
        for zone in ("Asia/Seoul", "Europe/Berlin"):
            with self.subTest(zone=zone), machine_zone(zone):
                self.assertEqual(exchange.ics_time("2026-10-12T22:00:00Z", False), ":20261012T220000Z")


class ExchangeTokenSave(unittest.TestCase):
    def setUp(self):
        self.exchange = load_script("mailday-exchange")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.exchange.TOKEN_FILE = Path(self.tmp.name) / "sub" / "token.gpg"

    def fake_gpg(self, after_write=None):
        calls = []

        def run(command, **kwargs):
            out = Path(command[command.index("-o") + 1])
            calls.append(out)
            out.write_bytes(b"encrypted:" + kwargs["input"])
            if after_write:
                after_write(len(calls))
            return subprocess.CompletedProcess(command, 0, b"", b"")
        return run, calls

    def test_overlapping_saves_do_not_share_a_temp_file(self):
        def second_save_in_the_middle_of_the_first(number):
            if number == 1:
                self.exchange.save_token({"access_token": "second"})
        run, calls = self.fake_gpg(second_save_in_the_middle_of_the_first)
        with mock.patch.object(self.exchange.subprocess, "run", run):
            self.exchange.save_token({"access_token": "first"})
        self.assertEqual(len(calls), 2)
        self.assertNotEqual(calls[0], calls[1])
        self.assertTrue(self.exchange.TOKEN_FILE.exists())
        self.assertEqual(os.stat(self.exchange.TOKEN_FILE).st_mode & 0o777, 0o600)
        self.assertEqual(os.listdir(self.exchange.TOKEN_FILE.parent), ["token.gpg"])

    def test_gpg_failure_is_one_line_exit_and_leaves_no_temp_file(self):
        def run(command, **kwargs):
            raise subprocess.CalledProcessError(2, command, stderr=b"gpg: no default secret key\nsecond line\n")
        with mock.patch.object(self.exchange.subprocess, "run", run):
            with self.assertRaises(SystemExit) as caught:
                self.exchange.save_token({"access_token": "x"})
        message = str(caught.exception.code)
        self.assertIsInstance(caught.exception.code, str)  # sys.exit(str): message on stderr, status 1
        self.assertIn("second line", message)
        self.assertNotIn("\n", message)
        self.assertEqual(os.listdir(self.exchange.TOKEN_FILE.parent), [])

    def test_gpg_failure_on_load_is_one_line_exit(self):
        self.exchange.TOKEN_FILE.parent.mkdir(parents=True)
        self.exchange.TOKEN_FILE.write_bytes(b"x")

        def run(command, **kwargs):
            raise subprocess.CalledProcessError(2, command, stderr=b"gpg: decryption failed\n")
        with mock.patch.object(self.exchange.subprocess, "run", run):
            with self.assertRaises(SystemExit) as caught:
                self.exchange.load_token()
        self.assertIn("decryption failed", str(caught.exception.code))


def calendar_item(item_id: str, start: str = "2026-10-12T08:00:00Z") -> str:
    return (f'<t:CalendarItem><t:ItemId Id="{item_id}" ChangeKey="k"/><t:Subject>{item_id}</t:Subject>'
            f"<t:Start>{start}</t:Start><t:End>{start}</t:End></t:CalendarItem>")


def find_item_response(items: list[str], last: bool | None) -> ET.Element:
    attribute = "" if last is None else f' IncludesLastItemInRange="{str(last).lower()}"'
    return ET.fromstring(
        '<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" '
        'xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" '
        'xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"><s:Body><m:FindItemResponse>'
        '<m:ResponseMessages><m:FindItemResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>'
        f'<m:RootFolder TotalItemsInView="{len(items)}"{attribute}><t:Items>{"".join(items)}</t:Items></m:RootFolder>'
        "</m:FindItemResponseMessage></m:ResponseMessages></m:FindItemResponse></s:Body></s:Envelope>")


class ExchangeFindItems(unittest.TestCase):
    def setUp(self):
        self.exchange = load_script("mailday-exchange")

    def test_parse_reports_whether_the_window_was_complete(self):
        for last, want in [(True, True), (False, False), (None, True)]:
            with self.subTest(last=last):
                items, complete = self.exchange.parse_find_items(find_item_response([calendar_item("a")], last))
                self.assertEqual(len(items), 1)
                self.assertIs(complete, want)

    def fake_window(self, calendar, cap, calls):
        """calendar: {id: (start day, end day)}; a window returns at most cap items overlapping it."""
        def window(bearer, start, end):
            calls.append((start, end))
            first, last = start.timestamp() / 86400, end.timestamp() / 86400
            hits = [i for i, (s, e) in calendar.items() if s < last and e >= first]
            return [ET.fromstring(calendar_item(i).replace("<t:CalendarItem>", '<t:CalendarItem xmlns:t="%s">' % self.exchange.NS["t"]))
                    for i in hits[:cap]], len(hits) <= cap
        return window

    def test_a_calendar_over_the_limit_is_fetched_in_full_without_duplicates(self):
        import datetime as dt
        start = dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)
        base = start.timestamp() / 86400
        calendar = {f"e{n}": (base + n, base + n + 0.5) for n in range(40)}
        calendar["long"] = (base + 5, base + 30)  # overlaps many windows, must appear once
        calls = []
        with mock.patch.object(self.exchange, "find_items_window", self.fake_window(calendar, 6, calls)):
            items = self.exchange.find_items("b", start, start + dt.timedelta(days=64))
        ids = sorted(i.find("t:ItemId", self.exchange.NS).get("Id") for i in items)
        self.assertEqual(ids, sorted(calendar))
        self.assertGreater(len(calls), 1)

    def test_a_day_that_alone_is_over_the_limit_warns_on_stderr(self):
        import datetime as dt
        start = dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)
        err = io.StringIO()
        full = [ET.fromstring(calendar_item("a").replace("<t:CalendarItem>", '<t:CalendarItem xmlns:t="%s">' % self.exchange.NS["t"]))]
        with mock.patch.object(self.exchange, "find_items_window", lambda b, s, e: (full, False)), \
                contextlib.redirect_stderr(err):
            items = self.exchange.find_items("b", start, start + dt.timedelta(hours=20))
        self.assertEqual(len(items), 1)
        self.assertIn("warning", err.getvalue())
        self.assertIn("2026-01-01", err.getvalue())


# ---------------------------------------------------------------- mailday-calsync

class CalsyncEscape(unittest.TestCase):
    def test_lone_carriage_return_becomes_a_newline_escape(self):
        calsync = load_script("mailday-calsync")
        self.assertEqual(calsync.escape("a\r\nb\rc\nd"), "a\\nb\\nc\\nd")

    def test_injected_property_stays_inside_the_summary(self):
        calsync = load_script("mailday-calsync")
        event = {"id": "1", "start": {"date": "2026-10-12"}, "end": {"date": "2026-10-13"},
                 "summary": "Lunch\rX-MAILDAY-EDITABLE:TRUE", "description": "x\rSTATUS:CANCELLED"}
        text = "\r\n".join(calsync.fold(line) for line in calsync.vevent(event, "20260101T000000Z", "Work"))
        lines = text.replace("\r\n ", "").split("\r\n")
        self.assertEqual([line for line in lines if line.startswith(("STATUS:", "X-MAILDAY-EDITABLE"))],
                         ["X-MAILDAY-EDITABLE:TRUE"])  # the real one, emitted once (for Work)
        self.assertEqual(sum("\r" in line or "\n" in line for line in lines), 0)


def work_event(summary="Standup"):
    return {"id": "w1", "summary": summary, "start": {"dateTime": "2026-10-12T09:00:00+02:00"},
            "end": {"dateTime": "2026-10-12T09:30:00+02:00"}}


class CalsyncStamp(unittest.TestCase):
    def test_timed_is_utc_whatever_the_machine_zone(self):
        calsync = load_script("mailday-calsync")
        for zone in ("Asia/Seoul", "Europe/Berlin"):
            with self.subTest(zone=zone), machine_zone(zone):
                self.assertEqual(calsync.stamp({"dateTime": "2026-10-12T09:00:00+09:00"}), ":20261012T000000Z")
                # An offset-less time is read in the event's own zone, never the machine's.
                self.assertEqual(calsync.stamp({"dateTime": "2026-10-12T09:00:00", "timeZone": "Asia/Seoul"}),
                                 ":20261012T000000Z")

    def test_all_day_is_a_date(self):
        calsync = load_script("mailday-calsync")
        with machine_zone("Asia/Seoul"):
            self.assertEqual(calsync.stamp({"date": "2026-10-12"}), ";VALUE=DATE:20261012")


class CalsyncFailures(unittest.TestCase):
    def setUp(self):
        self.calsync = load_script("mailday-calsync")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.out = Path(self.tmp.name)

    def test_good_calendars_are_written_and_failures_are_returned(self):
        (self.out / "Family.ics").write_text("OLD")
        envelope = {"calendars": [{"name": "Work", "events": [work_event()]}],
                    "failures": [{"calendar": "Family", "error": "HTTP 503"}]}
        written, failures = self.calsync.write_calendars(envelope, self.out, "20260101T000000Z")
        self.assertEqual(written, ["Work=1"])
        self.assertEqual(len(failures), 1)
        self.assertIn("Family", failures[0])
        self.assertIn("HTTP 503", failures[0])
        self.assertIn("SUMMARY:Standup", (self.out / "Work.ics").read_text())
        self.assertEqual((self.out / "Family.ics").read_text(), "OLD")  # last good copy kept

    def test_no_failures_key_means_no_failures(self):
        envelope = {"calendars": [{"name": "Work", "events": []}]}
        self.assertEqual(self.calsync.write_calendars(envelope, self.out, "n"), (["Work=0"], []))

    def run_main(self, envelope):
        proc = subprocess.CompletedProcess([], 0, stdout=json.dumps(envelope), stderr="")
        env = {"MAILDAY_EXCHANGE_TOKEN": str(self.out / "no-such-token")}  # keeps the Exchange sync off
        gcal = self.out / "gcal.py"
        gcal.write_text("")
        err = io.StringIO()
        with mock.patch.object(self.calsync.subprocess, "run", return_value=proc), \
                mock.patch.object(self.calsync, "OUT", self.out), \
                mock.patch.object(self.calsync, "PUBLIC_FEEDS", {}), \
                mock.patch.object(self.calsync, "GCAL", gcal), \
                mock.patch.dict(os.environ, env), mock.patch.object(sys, "argv", ["mailday-calsync"]), \
                contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            try:
                self.calsync.main()
            except SystemExit as exit:
                return exit.code, err.getvalue()
        return None, err.getvalue()

    def test_main_exits_2_after_writing_the_rest_when_a_calendar_failed(self):
        code, stderr = self.run_main({"calendars": [{"name": "Work", "events": [work_event()]}],
                                      "failures": [{"calendar": "Family", "error": "boom"}]})
        self.assertEqual(code, 2)
        self.assertIn("Family", stderr)
        self.assertIn("boom", stderr)
        self.assertTrue((self.out / "Work.ics").exists())

    def test_main_exits_normally_when_everything_fetched(self):
        code, stderr = self.run_main({"calendars": [{"name": "Work", "events": [work_event()]}]})
        self.assertIsNone(code)
        self.assertEqual(stderr, "")


# ---------------------------------------------------------------- mailday-calendar

class CalendarTimes(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")

    def test_insert_body_has_no_null_keys(self):
        body = self.cal.google_times(make_args(start="2026-10-12", end="2026-10-13", all_day=True))
        self.assertEqual(body, {"start": {"date": "2026-10-12"}, "end": {"date": "2026-10-13"}})

    def test_patch_to_all_day_nulls_date_time(self):
        body = self.cal.google_times(make_args(start="2026-10-12", end="2026-10-13", all_day=True), patch=True)
        self.assertEqual(body["start"], {"date": "2026-10-12", "dateTime": None})
        self.assertEqual(body["end"], {"date": "2026-10-13", "dateTime": None})

    def test_google_time_zone_is_the_local_zone(self):
        for zone in ("Asia/Seoul", "Europe/Berlin"):
            with self.subTest(zone=zone), in_zone(self.cal, zone):
                body = self.cal.google_times(make_args(start="2026-10-12T09:00:00+09:00", end="2026-10-12T10:00:00+09:00"))
                self.assertEqual(body["start"]["timeZone"], zone)
                self.assertEqual(body["end"]["timeZone"], zone)

    def test_patch_to_timed_nulls_date(self):
        with in_zone(self.cal, "Europe/Berlin"):
            body = self.cal.google_times(make_args(start="2026-10-12T09:00:00+02:00", end="2026-10-12T10:00:00+02:00", timed=True), patch=True)
        self.assertEqual(body["start"], {"dateTime": "2026-10-12T09:00:00+02:00", "timeZone": "Europe/Berlin", "date": None})
        self.assertIsNone(body["end"]["date"])

    def test_update_sends_the_nulls(self):
        svc = FakeGoogle(patch={"id": "ev1"})
        with mock.patch.object(self.cal, "google", return_value=(FAKE_GCAL, svc)):
            self.cal.google_update(make_args(start="2026-10-12", end="2026-10-13", all_day=True))
        body = svc.kwargs("patch")["body"]
        self.assertIn("dateTime", body["start"])
        self.assertIsNone(body["start"]["dateTime"])


class CalendarUntil(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")

    def test_all_day_google_repeat_gets_a_date_until(self):
        rule = "FREQ=WEEKLY;BYDAY=MO;UNTIL=20261220T225959Z"
        with in_zone(self.cal, "Europe/Berlin"):
            self.assertEqual(self.cal.google_rrule(rule, True), "FREQ=WEEKLY;BYDAY=MO;UNTIL=20261220")
        self.assertEqual(self.cal.google_rrule(rule, False), rule)
        self.assertEqual(self.cal.google_rrule("FREQ=DAILY;COUNT=3", True), "FREQ=DAILY;COUNT=3")
        self.assertEqual(self.cal.google_rrule("FREQ=DAILY;UNTIL=20261220", True), "FREQ=DAILY;UNTIL=20261220")

    def test_until_is_the_date_in_the_local_zone(self):
        # 23:00Z on 20 Dec is 00:00 on the 21st in Berlin, 08:00 on the 21st in Seoul;
        # 14:59:59Z is still the 20th in Berlin and 23:59:59 on the 20th in Seoul.
        for zone, late, early in (("Europe/Berlin", "20261221", "20261220"), ("Asia/Seoul", "20261221", "20261220"),
                                  ("America/Los_Angeles", "20261220", "20261220")):
            with self.subTest(zone=zone), in_zone(self.cal, zone):
                self.assertEqual(self.cal.google_rrule("FREQ=DAILY;UNTIL=20261220T230000Z", True), f"FREQ=DAILY;UNTIL={late}")
                self.assertEqual(self.cal.google_rrule("FREQ=DAILY;UNTIL=20261220T145959Z", True), f"FREQ=DAILY;UNTIL={early}")

    def test_until_near_midnight_differs_between_seoul_and_berlin(self):
        rule = "FREQ=DAILY;UNTIL=20261220T150000Z"  # 16:00 Berlin, but 00:00 on the 21st in Seoul
        with in_zone(self.cal, "Europe/Berlin"):
            self.assertEqual(self.cal.google_rrule(rule, True), "FREQ=DAILY;UNTIL=20261220")
        with in_zone(self.cal, "Asia/Seoul"):
            self.assertEqual(self.cal.google_rrule(rule, True), "FREQ=DAILY;UNTIL=20261221")

    def test_create_sends_the_date_until(self):
        svc = FakeGoogle(insert={"id": "n"})
        args = make_args(title="T", start="2026-10-12", end="2026-10-13", all_day=True,
                         rrule="FREQ=WEEKLY;UNTIL=20261220T225959Z")
        with mock.patch.object(self.cal, "google", return_value=(FAKE_GCAL, svc)), in_zone(self.cal, "Europe/Berlin"):
            self.cal.google_create(args)
        self.assertEqual(svc.kwargs("insert")["body"]["recurrence"], ["RRULE:FREQ=WEEKLY;UNTIL=20261220"])


class CalendarLocalZone(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")

    def test_tz_variable_wins_and_a_leading_colon_is_stripped(self):
        with mock.patch.dict(os.environ, {"TZ": ":Asia/Seoul"}):
            self.assertEqual(self.cal.local_zone()[0], "Asia/Seoul")
            self.assertEqual(str(self.cal.local_zone()[1]), "Asia/Seoul")
        with mock.patch.dict(os.environ, {"TZ": "Europe/Berlin"}):
            self.assertEqual(self.cal.local_zone()[0], "Europe/Berlin")

    def test_etc_localtime_link_is_read_when_tz_is_unset(self):
        env = {k: v for k, v in os.environ.items() if k != "TZ"}
        for link, expected in (("/usr/share/zoneinfo/Asia/Seoul", "Asia/Seoul"),
                               ("/var/db/timezone/zoneinfo/America/New_York", "America/New_York")):
            with self.subTest(link=link), mock.patch.dict(os.environ, env, clear=True), \
                    mock.patch("os.path.realpath", lambda path, link=link: link):
                self.assertEqual(self.cal.local_zone()[0], expected)

    def test_falls_back_to_utc(self):
        env = {k: v for k, v in os.environ.items() if k != "TZ"}
        with mock.patch.dict(os.environ, env, clear=True), mock.patch("os.path.realpath", lambda path: "/etc/localtime"):
            self.assertEqual(self.cal.local_zone()[0], "UTC")
        # A POSIX TZ string is not an IANA name: skip it, and an unreadable link too.
        with mock.patch.dict(os.environ, {"TZ": "CET-1CEST"}), mock.patch("os.path.realpath", lambda path: "/etc/localtime"):
            self.assertEqual(self.cal.local_zone()[0], "UTC")

    def test_home_zone_from_the_config_is_the_fallback(self):
        cal = load_script("mailday-calendar", config='[calendar]\nhome_zone = "Europe/Berlin"\n')
        env = {k: v for k, v in os.environ.items() if k != "TZ"}
        with mock.patch.dict(os.environ, env, clear=True), mock.patch("os.path.realpath", lambda path: "/etc/localtime"):
            self.assertEqual(cal.local_zone()[0], "Europe/Berlin")

    def test_windows_ids(self):
        expected = {"Europe/Berlin": "W. Europe Standard Time", "Europe/Paris": "Romance Standard Time",
                    "Europe/Prague": "Central Europe Standard Time", "Europe/Warsaw": "Central European Standard Time",
                    "Europe/London": "GMT Standard Time", "Asia/Seoul": "Korea Standard Time",
                    "Asia/Tokyo": "Tokyo Standard Time", "Asia/Hong_Kong": "China Standard Time",
                    "America/Los_Angeles": "Pacific Standard Time", "Etc/UTC": "UTC"}
        for iana, windows in expected.items():
            with self.subTest(zone=iana), in_zone(self.cal, iana):
                self.assertEqual(self.cal.ews_zone()[0], windows)
        for iana in self.cal.WINDOWS_ZONES:  # every name in the table is real
            self.assertEqual(self.cal.ZoneInfo(iana).key, iana)

    def test_unknown_zone_is_utc_with_one_warning(self):
        err = io.StringIO()
        with in_zone(self.cal, "Pacific/Auckland"), contextlib.redirect_stderr(err):
            self.assertEqual(self.cal.ews_zone()[0], "UTC")
            self.assertEqual(str(self.cal.ews_zone()[1]), "UTC")
        self.assertEqual(err.getvalue().count("Pacific/Auckland"), 1)

    def test_unknown_zone_sends_ews_times_in_utc(self):
        # 09:00 on the 13th in Auckland (+13) is 20:00 on the 12th UTC: the recurrence date is the UTC one.
        with in_zone(self.cal, "Pacific/Auckland"), contextlib.redirect_stderr(io.StringIO()):
            xml = self.cal.ews_recurrence("FREQ=DAILY;UNTIL=20261220T230000Z", "2026-10-13T09:00:00+13:00")
            moment = self.cal.ews_time("2026-10-13T09:00:00+13:00", False)
        self.assertIn("<t:StartDate>2026-10-12</t:StartDate>", xml)
        self.assertIn("<t:EndDate>2026-12-20</t:EndDate>", xml)
        self.assertEqual(moment, "2026-10-12T20:00:00Z")

    def test_create_header_and_recurrence_zone_ids_agree(self):
        for zone, windows in (("Asia/Seoul", "Korea Standard Time"), ("Europe/Berlin", "W. Europe Standard Time")):
            calls = []
            with self.subTest(zone=zone), in_zone(self.cal, zone), \
                    mock.patch.object(self.cal, "ews", ews_fake(calls)):
                self.cal.exchange_create(make_args(title="T", start="2026-10-12T09:00:00+09:00",
                                                   end="2026-10-12T10:00:00+09:00", rrule="FREQ=DAILY;COUNT=2"))
                self.assertIn(f'<t:StartTimeZone Id="{windows}"/><t:EndTimeZone Id="{windows}"/>', calls[0])

    def test_envelope_header_uses_the_windows_id(self):
        exchange = load_script("mailday-exchange")
        exchange.access_token = lambda: "token"
        sent = []

        def urlopen(request, timeout=None):
            sent.append(request.data.decode())
            return FakeResponse(GET_ITEM)
        for zone, windows in (("Asia/Seoul", "Korea Standard Time"), ("Europe/Berlin", "W. Europe Standard Time")):
            with self.subTest(zone=zone), in_zone(self.cal, zone), \
                    mock.patch.object(self.cal, "load", lambda path, name: exchange), \
                    mock.patch("urllib.request.urlopen", urlopen):
                self.cal.ews("<m:GetItem/>")
            self.assertIn(f'<t:TimeZoneDefinition Id="{windows}"/>', sent[-1])


class CalendarRecurrenceZone(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")

    def test_first_date_is_the_local_date_of_a_start_in_another_offset(self):
        # 23:30 on Mon 12 Oct in Los Angeles (-07:00) is 08:30 on Tue 13 Oct in Berlin
        # and 15:30 on Tue 13 Oct in Seoul; 09:30 on Mon 12 in Los Angeles is Tue 13 01:30 in Seoul.
        for zone, start, day, weekday in (("Europe/Berlin", "2026-10-12T23:30:00-07:00", "13", "Tuesday"),
                                          ("Asia/Seoul", "2026-10-12T23:30:00-07:00", "13", "Tuesday"),
                                          ("Europe/Berlin", "2026-10-12T09:30:00-07:00", "12", "Monday"),
                                          ("Asia/Seoul", "2026-10-12T09:30:00-07:00", "13", "Tuesday")):
            with self.subTest(zone=zone, start=start), in_zone(self.cal, zone):
                weekly = self.cal.ews_recurrence("FREQ=WEEKLY", start)
                self.assertIn(f"<t:DaysOfWeek>{weekday}</t:DaysOfWeek>", weekly)
                self.assertIn(f"<t:StartDate>2026-10-{day}</t:StartDate>", weekly)
                monthly = self.cal.ews_recurrence("FREQ=MONTHLY;COUNT=2", start)
                self.assertIn(f"<t:DayOfMonth>{int(day)}</t:DayOfMonth>", monthly)
                self.assertIn(f"<t:StartDate>2026-10-{day}</t:StartDate>", monthly)

    def test_first_date_near_midnight_in_seoul_and_berlin(self):
        # 00:30 on the 13th in Seoul (+09:00) is 17:30 on the 12th in Berlin.
        start = "2026-10-13T00:30:00+09:00"
        with in_zone(self.cal, "Asia/Seoul"):
            self.assertIn("<t:StartDate>2026-10-13</t:StartDate>", self.cal.ews_recurrence("FREQ=DAILY", start))
        with in_zone(self.cal, "Europe/Berlin"):
            self.assertIn("<t:StartDate>2026-10-12</t:StartDate>", self.cal.ews_recurrence("FREQ=DAILY", start))

    def test_until_date_is_in_the_local_zone(self):
        for zone, end in (("Europe/Berlin", "2026-12-21"), ("Asia/Seoul", "2026-12-21"), ("America/Los_Angeles", "2026-12-20")):
            with self.subTest(zone=zone), in_zone(self.cal, zone):
                xml = self.cal.ews_recurrence("FREQ=DAILY;UNTIL=20261220T230000Z", "2026-10-12T09:00:00+02:00")
                self.assertIn(f"<t:EndDate>{end}</t:EndDate>", xml)

    def test_until_near_midnight_differs_between_seoul_and_berlin(self):
        rule = "FREQ=DAILY;UNTIL=20261220T150000Z"
        with in_zone(self.cal, "Europe/Berlin"):
            self.assertIn("<t:EndDate>2026-12-20</t:EndDate>", self.cal.ews_recurrence(rule, "2026-10-12T09:00:00+02:00"))
        with in_zone(self.cal, "Asia/Seoul"):
            self.assertIn("<t:EndDate>2026-12-21</t:EndDate>", self.cal.ews_recurrence(rule, "2026-10-12T09:00:00+02:00"))

    def test_all_day_start_is_a_date_already(self):
        for zone in ("America/Los_Angeles", "Asia/Seoul", "Europe/Berlin"):
            with self.subTest(zone=zone), in_zone(self.cal, zone), contextlib.redirect_stderr(io.StringIO()):
                xml = self.cal.ews_recurrence("FREQ=WEEKLY", "2026-10-12", all_day=True)
                self.assertIn("<t:StartDate>2026-10-12</t:StartDate>", xml)
                self.assertIn("Monday", xml)

    def test_a_time_without_an_offset_is_wall_clock_in_the_local_zone(self):
        with in_zone(self.cal, "Asia/Seoul"):
            self.assertEqual(self.cal.ews_time("2026-10-13T09:00:00", False), "2026-10-13T00:00:00Z")
        with in_zone(self.cal, "Europe/Berlin"):
            self.assertEqual(self.cal.ews_time("2026-10-13T09:00:00", False), "2026-10-13T07:00:00Z")


FREE_BUSY = """<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"
  xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages"
  xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"><s:Body>
<m:GetUserAvailabilityResponse><m:FreeBusyResponseArray>
<m:FreeBusyResponse><m:ResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode></m:ResponseMessage>
  <m:FreeBusyView><t:FreeBusyViewType>Detailed</t:FreeBusyViewType><t:CalendarEventArray>
    <t:CalendarEvent><t:StartTime>2026-10-12T10:00:00</t:StartTime><t:EndTime>2026-10-12T11:00:00</t:EndTime><t:BusyType>Busy</t:BusyType></t:CalendarEvent>
    <t:CalendarEvent><t:StartTime>2026-10-12T12:00:00</t:StartTime><t:EndTime>2026-10-12T13:00:00</t:EndTime><t:BusyType>Free</t:BusyType></t:CalendarEvent>
  </t:CalendarEventArray></m:FreeBusyView></m:FreeBusyResponse>
<m:FreeBusyResponse><m:ResponseMessage ResponseClass="Error"><m:MessageText>No mailbox for this address.</m:MessageText>
  <m:ResponseCode>ErrorMailRecipientNotFound</m:ResponseCode></m:ResponseMessage></m:FreeBusyResponse>
</m:FreeBusyResponseArray></m:GetUserAvailabilityResponse></s:Body></s:Envelope>"""


class FakeResponse:
    def __init__(self, data: str):
        self.data = data.encode()

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def read(self):
        return self.data


class CalendarFreeBusy(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")
        exchange = load_script("mailday-exchange")
        exchange.access_token = lambda: "token"
        patches = [mock.patch.object(self.cal, "load", lambda path, name: exchange),
                   mock.patch("urllib.request.urlopen", lambda request, timeout=None: FakeResponse(FREE_BUSY))]
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def freebusy(self, zone: str, start: str, end: str):
        """Run exchange_freebusy in zone; returns (result, request XML sent)."""
        sent = []

        def urlopen(request, timeout=None):
            sent.append(request.data.decode())
            return FakeResponse(FREE_BUSY)
        args = make_args(attendee=["a@uni.example", "ghost@uni.example"], start=start, end=end)
        with in_zone(self.cal, zone), mock.patch("urllib.request.urlopen", urlopen):
            return self.cal.exchange_freebusy(args), sent[0]

    def test_one_address_without_free_busy_is_reported_not_fatal(self):
        result, _ = self.freebusy("Europe/Berlin", "2026-10-12T00:00:00+02:00", "2026-10-13T00:00:00+02:00")
        self.assertEqual(result["busy"], {"a@uni.example": [["2026-10-12T08:00:00Z", "2026-10-12T09:00:00Z"]]})
        self.assertEqual(result["errors"], {"ghost@uni.example": "No mailbox for this address."})

    def test_berlin_request_and_returned_times(self):
        result, xml = self.freebusy("Europe/Berlin", "2026-10-12T00:00:00+02:00", "2026-10-13T00:00:00+02:00")
        self.assertIn('<t:TimeZoneDefinition Id="W. Europe Standard Time"/>', xml)
        self.assertIn("<t:StartTime>2026-10-12T00:00:00</t:StartTime><t:EndTime>2026-10-13T00:00:00</t:EndTime>", xml)
        # The same XML Exchange was sent before the zone became dynamic.
        self.assertIn("<t:TimeZone><t:Bias>-60</t:Bias><t:StandardTime><t:Bias>0</t:Bias><t:Time>03:00:00</t:Time>"
                      "<t:DayOrder>5</t:DayOrder><t:Month>10</t:Month><t:DayOfWeek>Sunday</t:DayOfWeek></t:StandardTime>"
                      "<t:DaylightTime><t:Bias>-60</t:Bias><t:Time>02:00:00</t:Time><t:DayOrder>5</t:DayOrder>"
                      "<t:Month>3</t:Month><t:DayOfWeek>Sunday</t:DayOfWeek></t:DaylightTime></t:TimeZone>", xml)
        # Returned naive 10:00-11:00 on the 12th is CEST, 08:00Z.
        self.assertEqual(result["busy"]["a@uni.example"], [["2026-10-12T08:00:00Z", "2026-10-12T09:00:00Z"]])

    def test_seoul_request_and_returned_times(self):
        # The same instants as the Berlin test, asked in Seoul's wall clock.
        result, xml = self.freebusy("Asia/Seoul", "2026-10-12T00:00:00+02:00", "2026-10-13T00:00:00+02:00")
        self.assertIn('<t:TimeZoneDefinition Id="Korea Standard Time"/>', xml)
        self.assertIn("<t:StartTime>2026-10-12T07:00:00</t:StartTime><t:EndTime>2026-10-13T07:00:00</t:EndTime>", xml)
        zone = ET.fromstring("<r xmlns:t='http://schemas.microsoft.com/exchange/services/2006/types'>"
                             + xml[xml.index("<t:TimeZone>"):xml.index("</t:TimeZone>") + len("</t:TimeZone>")] + "</r>")
        t = "{http://schemas.microsoft.com/exchange/services/2006/types}"
        self.assertEqual(zone.find(f".//{t}TimeZone/{t}Bias").text, "-540")
        standard, daylight = (zone.find(f".//{t}{name}") for name in ("StandardTime", "DaylightTime"))
        self.assertEqual(ET.tostring(standard).decode().replace("StandardTime", "X"),
                         ET.tostring(daylight).decode().replace("DaylightTime", "X"))
        self.assertEqual(standard.find(f"{t}Bias").text, "0")
        # Returned naive 10:00-11:00 on the 12th is KST: 01:00Z.
        self.assertEqual(result["busy"]["a@uni.example"], [["2026-10-12T01:00:00Z", "2026-10-12T02:00:00Z"]])

    def test_window_in_another_offset_is_converted_to_the_local_wall_clock(self):
        _, xml = self.freebusy("Asia/Seoul", "2026-10-12T00:00:00Z", "2026-10-12T12:00:00Z")
        self.assertIn("<t:StartTime>2026-10-12T09:00:00</t:StartTime><t:EndTime>2026-10-12T21:00:00</t:EndTime>", xml)

    def test_unknown_zone_asks_and_reads_in_utc(self):
        with contextlib.redirect_stderr(io.StringIO()):
            result, xml = self.freebusy("Pacific/Auckland", "2026-10-12T00:00:00+02:00", "2026-10-13T00:00:00+02:00")
        self.assertIn('<t:TimeZoneDefinition Id="UTC"/>', xml)
        self.assertIn("<t:StartTime>2026-10-11T22:00:00</t:StartTime>", xml)
        self.assertIn("<t:TimeZone><t:Bias>0</t:Bias>", xml)
        self.assertEqual(result["busy"]["a@uni.example"], [["2026-10-12T10:00:00Z", "2026-10-12T11:00:00Z"]])

    def test_other_ews_calls_still_fail_on_an_error_response(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out), self.assertRaises(SystemExit) as caught:
            self.cal.ews("<m:Whatever/>")
        self.assertEqual(caught.exception.code, 1)
        self.assertIn("No mailbox for this address.", json.loads(out.getvalue())["error"])


class CalendarTimeZoneXml(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")

    def rules(self, zone: str, year: int = 2026) -> dict:
        t = "{http://schemas.microsoft.com/exchange/services/2006/types}"
        root = ET.fromstring("<r xmlns:t='http://schemas.microsoft.com/exchange/services/2006/types'>"
                             f"{self.cal.ews_timezone_xml(self.cal.ZoneInfo(zone), year)}</r>")
        out = {"Bias": root.findtext(f"{t}Bias")}
        for name in ("StandardTime", "DaylightTime"):
            node = root.find(f"{t}{name}")
            out[name] = tuple(node.findtext(f"{t}{child}") for child in ("Bias", "Time", "DayOrder", "Month", "DayOfWeek"))
        return out

    def test_european_rules_follow_the_zone(self):
        self.assertEqual(self.rules("Europe/Berlin"), {"Bias": "-60",
                         "StandardTime": ("0", "03:00:00", "5", "10", "Sunday"),
                         "DaylightTime": ("-60", "02:00:00", "5", "3", "Sunday")})
        # London changes at 01:00 UTC, so 02:00 summer / 01:00 winter on the clock.
        self.assertEqual(self.rules("Europe/London"), {"Bias": "0",
                         "StandardTime": ("0", "02:00:00", "5", "10", "Sunday"),
                         "DaylightTime": ("-60", "01:00:00", "5", "3", "Sunday")})

    def test_us_rules(self):
        self.assertEqual(self.rules("America/New_York"), {"Bias": "300",
                         "StandardTime": ("0", "02:00:00", "1", "11", "Sunday"),
                         "DaylightTime": ("-60", "02:00:00", "2", "3", "Sunday")})
        self.assertEqual(self.rules("America/Los_Angeles")["Bias"], "480")

    def test_zones_without_daylight_time(self):
        for zone, bias in (("Asia/Seoul", "-540"), ("Asia/Tokyo", "-540"), ("Asia/Singapore", "-480"), ("UTC", "0")):
            with self.subTest(zone=zone):
                rules = self.rules(zone)
                self.assertEqual(rules["Bias"], bias)
                self.assertEqual(rules["StandardTime"], rules["DaylightTime"])
                self.assertEqual(rules["StandardTime"][0], "0")

    def test_southern_hemisphere_rules_are_not_swapped(self):
        rules = self.rules("Pacific/Auckland")
        self.assertEqual(rules["Bias"], "-720")
        self.assertEqual(rules["StandardTime"][3], "4")
        self.assertEqual(rules["DaylightTime"][3], "9")
        self.assertEqual(rules["DaylightTime"][0], "-60")


GET_ITEM = """<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"
  xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages"
  xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"><s:Body><m:GetItemResponse><m:ResponseMessages>
<m:GetItemResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode><m:Items>
<t:CalendarItem><t:ItemId Id="x"/><t:Body BodyType="Text">  Agenda: budget  </t:Body></t:CalendarItem>
</m:Items></m:GetItemResponseMessage></m:ResponseMessages></m:GetItemResponse></s:Body></s:Envelope>"""


def ews_fake(calls, body_xml=GET_ITEM):
    """A stand-in for mailday-calendar's ews(): records the request, answers GetItem with body_xml."""
    ns = {"m": "http://schemas.microsoft.com/exchange/services/2006/messages",
          "t": "http://schemas.microsoft.com/exchange/services/2006/types"}

    def ews(body, strict=True):
        calls.append(body)
        return ET.fromstring(body_xml if "GetItem" in body else GET_ITEM.replace("GetItem", "Other")), ns
    return ews


class CalendarTransfer(unittest.TestCase):
    def setUp(self):
        self.cal = load_script("mailday-calendar")
        self.backends = {"Work": "google", "Home": "google", "Outlook": "exchange"}

    def run_transfer(self, args, svc=None, created=None):
        """Run transfer(); return (result or error JSON, exit code)."""
        out = io.StringIO()
        svc = svc or FakeGoogle()
        with mock.patch.object(self.cal, "google", return_value=(FAKE_GCAL, svc)), contextlib.redirect_stdout(out):
            try:
                return self.cal.transfer(args, self.backends), 0
            except SystemExit as exit:
                return json.loads(out.getvalue().strip().splitlines()[-1]), exit.code

    def cross(self, **kw):
        values = dict(to="Outlook", title="T", start="2026-10-12T09:00:00+02:00", end="2026-10-12T10:00:00+02:00")
        values.update(kw)
        return make_args(**values)

    def test_google_to_exchange_copies_notes_and_deletes_without_telling_guests(self):
        svc = FakeGoogle(get={"id": "ev1", "description": "Original notes"}, delete={})
        created, calls = [], []
        args = self.cross()
        with mock.patch.object(self.cal, "exchange_create", lambda a: created.append((a.calendar, a.notes)) or {"id": "x1"}):
            result, code = self.run_transfer(args, svc)
        self.assertEqual((result, code), ({"id": "x1"}, 0))
        self.assertEqual(created, [("Outlook", "Original notes")])
        self.assertEqual(svc.kwargs("get")["calendarId"], "work-id")
        self.assertEqual(svc.kwargs("delete")["sendUpdates"], "none")
        self.assertEqual(svc.kwargs("delete")["calendarId"], "work-id")

    def test_explicit_notes_are_not_overwritten_or_fetched(self):
        svc = FakeGoogle(delete={})
        created = []
        with mock.patch.object(self.cal, "exchange_create", lambda a: created.append(a.notes) or {"id": "x1"}):
            self.run_transfer(self.cross(notes="Mine"), svc)
        self.assertEqual(created, ["Mine"])
        self.assertNotIn("get", [name for name, _ in svc.calls])

    def test_failed_notes_fetch_stops_before_anything_changes(self):
        # Moving on would delete the original and its notes with it.
        svc = FakeGoogle(get=RuntimeError("quota"), delete={})
        created = []
        with mock.patch.object(self.cal, "exchange_create", lambda a: created.append(a.notes) or {"id": "x1"}):
            result, code = self.run_transfer(self.cross(), svc)
        self.assertEqual(code, 1)
        self.assertEqual(created, [])
        self.assertNotIn("delete", [name for name, _ in svc.calls])
        self.assertIn("quota", result["error"])

    def test_delete_failure_reports_the_event_in_both_calendars(self):
        svc = FakeGoogle(get={"description": ""}, delete=RuntimeError("403 forbidden"))
        with mock.patch.object(self.cal, "exchange_create", lambda a: {"id": "x1"}):
            result, code = self.run_transfer(self.cross(), svc)
        self.assertEqual(code, 1)
        self.assertIs(result["partial"], True)
        self.assertEqual(result["id"], "x1")
        self.assertIn("both calendars", result["error"])
        self.assertIn("x1", result["error"])
        self.assertIn("403 forbidden", result["error"])

    def test_exchange_to_google_reads_the_body_and_cancels_nobody(self):
        calls = []
        svc = FakeGoogle(insert={"id": "g1", "htmlLink": ""})
        args = self.cross(calendar="Outlook", to="Work", id="exch-id")
        with mock.patch.object(self.cal, "ews", ews_fake(calls)):
            result, code = self.run_transfer(args, svc)
        self.assertEqual((result, code), ({"id": "g1"}, 0))
        self.assertEqual(svc.kwargs("insert")["body"]["description"], "Agenda: budget")
        self.assertIn("<m:GetItem>", calls[0])
        self.assertIn('Id="exch-id"', calls[0])
        self.assertIn('SendMeetingCancellations="SendToNone"', calls[-1])
        self.assertNotIn("SendToAll", calls[-1])

    def test_exchange_delete_failure_is_partial(self):
        def ews(body, strict=True):
            if "DeleteItem" in body:
                self.cal.fail("EWS: The specified object was not found")
            return ews_fake([])(body, strict)
        svc = FakeGoogle(insert={"id": "g1", "htmlLink": ""})
        with mock.patch.object(self.cal, "ews", ews):
            result, code = self.run_transfer(self.cross(calendar="Outlook", to="Work", id="exch-id"), svc)
        self.assertEqual(code, 1)
        self.assertIs(result["partial"], True)
        self.assertEqual(result["id"], "g1")
        self.assertIn("not found", result["error"])

    def test_same_service_move_does_not_patch_the_description_unless_asked(self):
        for notes, expect in [(None, False), ("New notes", True)]:
            with self.subTest(notes=notes):
                svc = FakeGoogle(move={"id": "moved"}, patch={"id": "moved"})
                args = make_args(to="Home", title="T", start="2026-10-12T09:00:00+02:00",
                                 end="2026-10-12T10:00:00+02:00", notes=notes)
                result, code = self.run_transfer(args, svc)
                self.assertEqual((result, code), ({"id": "moved"}, 0))
                self.assertEqual("description" in svc.kwargs("patch")["body"], expect)
                self.assertNotIn("get", [name for name, _ in svc.calls])


# ---------------------------------------------------------------- config file

FULL_CONFIG = """
[calendar]
feeds = [ { name = "Holidays", url = "https://feeds.example/holidays.ics" } ]
google_script = "/nonexistent/gcal.py"
google_own = ["Me@home.example", "me@work.example"]
home_zone = "Europe/Berlin"

[exchange]
calendar = "Work"
user = "me@uni.example"
token = "~/tokens/ews.gpg"
ews_url = "https://ews.example/EWS/Exchange.asmx"
mailbox_tz = "Asia/Seoul"
"""


class ConfigFile(unittest.TestCase):
    def test_file_is_read(self):
        calsync = load_script("mailday-calsync", FULL_CONFIG)
        self.assertEqual(calsync.PUBLIC_FEEDS, {"Holidays": "https://feeds.example/holidays.ics"})
        self.assertEqual(str(calsync.GCAL), "/nonexistent/gcal.py")
        self.assertEqual(calsync.OWN, {"me@home.example", "me@work.example"})
        exchange = load_script("mailday-exchange", FULL_CONFIG)
        self.assertEqual((exchange.CALENDAR, exchange.LOGIN, exchange.EWS), ("Work", "me@uni.example", "https://ews.example/EWS/Exchange.asmx"))
        self.assertEqual(exchange.TOKEN_FILE, Path.home() / "tokens" / "ews.gpg")
        self.assertEqual(exchange.mailbox_zone().key, "Asia/Seoul")
        cal = load_script("mailday-calendar", FULL_CONFIG)
        self.assertEqual((cal.EXCHANGE_CALENDAR, cal.DEFAULT_ZONE, str(cal.GCAL)), ("Work", "Europe/Berlin", "/nonexistent/gcal.py"))
        self.assertEqual(cal.OWN, {"me@home.example", "me@work.example"})
        self.assertEqual(cal.exchange_user(), "me@uni.example")

    def test_environment_wins_over_the_file(self):
        env = {"GCAL_SCRIPT": "/env/gcal.py", "MAILDAY_GOOGLE_OWN": "x@env.example, y@env.example",
               "MAILDAY_EXCHANGE_USER": "env@uni.example", "MAILDAY_EXCHANGE_TOKEN": "/env/token.gpg",
               "MAILDAY_EXCHANGE_MAILBOX_TZ": "America/New_York"}
        calsync = load_script("mailday-calsync", FULL_CONFIG, env)
        self.assertEqual(str(calsync.GCAL), "/env/gcal.py")
        self.assertEqual(calsync.OWN, {"x@env.example", "y@env.example"})
        exchange = load_script("mailday-exchange", FULL_CONFIG, env)
        self.assertEqual((exchange.LOGIN, str(exchange.TOKEN_FILE)), ("env@uni.example", "/env/token.gpg"))
        cal = load_script("mailday-calendar", FULL_CONFIG, env)
        self.assertEqual(str(cal.GCAL), "/env/gcal.py")
        self.assertEqual(cal.OWN, {"x@env.example", "y@env.example"})
        with mock.patch.dict(os.environ, env):  # read when called, so the test sets it again
            self.assertEqual(cal.exchange_user(), "env@uni.example")
            self.assertEqual(cal.mailbox_zone().key, "America/New_York")

    def test_missing_file_means_defaults(self):
        calsync = load_script("mailday-calsync")
        self.assertEqual((calsync.PUBLIC_FEEDS, calsync.GCAL, calsync.OWN), ({}, None, set()))
        exchange = load_script("mailday-exchange")
        self.assertEqual((exchange.CALENDAR, exchange.LOGIN, exchange.EWS, exchange.CLIENT_ID),
                         ("Outlook", "", "https://outlook.office365.com/EWS/Exchange.asmx",
                          "9e5f94bc-e8a4-4e73-b8be-63364c29d753"))
        self.assertEqual(exchange.TOKEN_FILE, Path.home() / ".config/mailday/ews-token.gpg")
        cal = load_script("mailday-calendar")
        self.assertEqual((cal.EXCHANGE_CALENDAR, cal.DEFAULT_ZONE, cal.GCAL, cal.OWN, cal.exchange_user()),
                         ("Outlook", "UTC", None, set(), ""))

    def test_xdg_config_home_is_honoured(self):
        with tempfile.TemporaryDirectory() as home:
            (Path(home) / "mailday").mkdir()
            (Path(home) / "mailday" / "config.toml").write_text('[exchange]\ncalendar = "FromXdg"\n')
            environment = {k: v for k, v in os.environ.items() if k not in CONFIG_ENV}
            environment["XDG_CONFIG_HOME"] = home
            loader = importlib.machinery.SourceFileLoader("xdg_exchange", str(SCRIPTS / "mailday-exchange"))
            module = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
            with mock.patch.dict(os.environ, environment, clear=True):
                loader.exec_module(module)
            self.assertEqual(module.CALENDAR, "FromXdg")

    def test_broken_file_is_a_one_line_exit(self):
        with self.assertRaises(SystemExit) as caught:
            load_script("mailday-exchange", "[exchange\n")
        self.assertIn("cannot read", str(caught.exception))


class ExchangeOff(unittest.TestCase):
    def test_authorize_refuses_without_a_user(self):
        exchange = load_script("mailday-exchange")
        with mock.patch.object(exchange.http.server, "HTTPServer", side_effect=AssertionError("started a login")), \
                self.assertRaises(SystemExit) as caught:
            exchange.authorize()
        self.assertIn("Exchange is off", str(caught.exception))
        self.assertIn("[exchange]", str(caught.exception))

    def test_calendar_hides_exchange_without_a_user(self):
        off = load_script("mailday-calendar")
        self.assertEqual(off.writable(), [])
        on = load_script("mailday-calendar", '[exchange]\nuser = "me@uni.example"\ncalendar = "Work"\n')
        self.assertEqual(on.writable(), [{"name": "Work", "backend": "exchange"}])

    def run_calsync(self, config, token_exists):
        calsync = load_script("mailday-calsync", config)
        with tempfile.TemporaryDirectory() as tmp:
            token = Path(tmp) / "token.gpg"
            if token_exists:
                token.write_text("x")
            ran = []

            def run(command, **kwargs):
                ran.append(command)
                return subprocess.CompletedProcess(command, 0, stdout="synced", stderr="")
            with mock.patch.object(calsync.subprocess, "run", run), mock.patch.object(calsync, "OUT", Path(tmp)), \
                    mock.patch.dict(os.environ, {"MAILDAY_EXCHANGE_TOKEN": str(token)}), \
                    mock.patch.object(sys, "argv", ["mailday-calsync"]), contextlib.redirect_stdout(io.StringIO()):
                calsync.main()
        return ran

    def test_calsync_syncs_exchange_only_with_user_and_token(self):
        self.assertEqual(self.run_calsync("", True), [])
        self.assertEqual(self.run_calsync('[exchange]\nuser = "me@uni.example"\n', False), [])
        ran = self.run_calsync('[exchange]\nuser = "me@uni.example"\n', True)
        self.assertEqual([c[-1] for c in ran], ["sync"])

    def test_calsync_notes_a_missing_google_script_once(self):
        calsync = load_script("mailday-calsync", '[calendar]\ngoogle_script = "/nonexistent/gcal.py"\n')
        err = io.StringIO()
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(calsync, "OUT", Path(tmp)), \
                mock.patch.dict(os.environ, {"MAILDAY_EXCHANGE_TOKEN": str(Path(tmp) / "none")}), \
                mock.patch.object(sys, "argv", ["mailday-calsync"]), \
                contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            calsync.main()
        self.assertEqual(err.getvalue().count("google_script"), 1)
        quiet = load_script("mailday-calsync")
        err = io.StringIO()
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(quiet, "OUT", Path(tmp)), \
                mock.patch.object(sys, "argv", ["mailday-calsync"]), \
                contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            quiet.main()
        self.assertEqual(err.getvalue(), "")


if __name__ == "__main__":
    unittest.main()


class CalendarAllDayInMailboxZone(unittest.TestCase):
    """An all-day Outlook event made from another country still lands on its date."""

    def test_all_day_is_mailbox_midnight_in_utc(self):
        cal = load_script("mailday-calendar", config='[exchange]\nmailbox_tz = "Europe/Berlin"\n')
        for zone in ("Asia/Seoul", "Europe/Berlin", "America/Los_Angeles"):
            with in_zone(cal, zone):
                self.assertEqual(cal.ews_time("2026-10-20", True), "2026-10-19T22:00:00Z")   # CEST
                self.assertEqual(cal.ews_time("2026-12-20", True), "2026-12-19T23:00:00Z")   # CET
