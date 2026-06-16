#!/usr/bin/env python3
"""Quick test for /ws/shipments/nearby sender mode."""
import asyncio
import json
import sys

try:
    import websockets
except ImportError:
    print("pip install websockets")
    sys.exit(1)

LAT = 32.646625
LNG = 51.668103

async def run(radius_km: float, limit: int):
    uri = "ws://localhost:8080/ws/shipments/nearby"
    async with websockets.connect(uri) as ws:
        connected = json.loads(await ws.recv())
        print("connected:", connected)

        req = {
            "type": "sender",
            "lat": LAT,
            "lng": LNG,
            "radius_km": radius_km,
            "limit": limit,
        }
        await ws.send(json.dumps(req))
        resp = json.loads(await ws.recv())
        print(json.dumps(resp, indent=2, ensure_ascii=False))

        if resp.get("type") == "driver.nearby":
            drivers = resp.get("drivers", [])
            print(f"\n--- summary: {resp.get('count')} drivers within {resp['query']['radius_km']} km (limit {resp['query']['limit']}) ---")
            for d in drivers[:10]:
                print(f"  {d['id']}: {d['distance_km']:.3f} km")
            if len(drivers) > 10:
                print(f"  ... and {len(drivers) - 10} more")

if __name__ == "__main__":
    radius = float(sys.argv[1]) if len(sys.argv) > 1 else 20
    limit = int(sys.argv[2]) if len(sys.argv) > 2 else 100
    asyncio.run(run(radius, limit))
