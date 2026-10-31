# Draft: request for airplanes.live API access

To: contact@airplanes.live
Subject: API access request: small personal flight-tracking project (FullFlight)

Hello,

I'd like to ask for access to the airplanes.live API (api.airplanes.live/v2) for a small personal project. Requests
to it currently return HTTP 403 with the message asking me to contact you, so I'm doing that and have not tried to
work around it.

**The project**

FullFlight is a self-hosted web page that I run at home, for myself only. It follows a short list of specific
aircraft that I choose and puts two kinds of information on one map: their ADS-B positions, and the ACARS / ADS-C /
CPDLC datalink messages they send (decoded with libacars, fetched from airframes.io). It also works out where an
aircraft probably is when ADS-B has gone quiet, for example in mid-ocean or after it has landed out of coverage.

- Project: [link to the repository, or "not published"]
- Not public, not commercial, no advertising, no redistribution of your data. I am the only user.
- For each watched aircraft it stores the positions it receives in a local SQLite database, for showing the track of
  its recent flights. Nothing is republished or shared.

**How it would use the API**

- Lookups by registration, hex and callsign (`/v2/reg/`, `/v2/hex/`, `/v2/callsign/`), for the handful of aircraft I am
  watching at any time (usually under a dozen).
- It is a fallback, not the main source: it asks adsb.lol and adsb.fi first, and would only ask airplanes.live when
  they have no answer or an incomplete one (no position or no callsign). That is mainly for aircraft the other two have
  not heard.
- No more than one request per second to your servers, and in practice much less than that. Each watched aircraft is
  polled about every 45 seconds while it is being followed.
- If your server answers 401, 403 or 429, it stops asking for an hour (30 seconds for 429, or longer if you send
  `Retry-After`) instead of retrying.
- It sends the User-Agent `FullFlight/0.1 (personal flight tracker)`. I can change that to anything you would like me
  to use, and add a contact address to it.

**What I am asking for**

Whatever permission or key you need to give me for this use, and your terms (rate limits, attribution) so I can follow
them. If a different endpoint or a different way of getting this data suits you better, I'm glad to use that.

Thank you for running the service, and for your time.

[Your name]
[Your email address]
