# Files from devicetype-library

These files are unchanged copies from [devicetype-library](https://github.com/netbox-community/devicetype-library), at commit `e160491884f3c56d2daef3431d52f56728971b85` of 2026-11-01, in the library's own layout. The library is dedicated to the public domain under CC0 1.0 Universal, a copy of which is in [LICENSE.txt](LICENSE.txt). Files that break rules on purpose are written by hand in the tests instead, so that these stay as the library has them.

To check a copy, or bring one up to date, read it from the library at a commit:

```sh
git -C .cache/devicetype-library show e160491884f3c56d2daef3431d52f56728971b85:device-types/Huawei/NE8000-X8.yaml
```

| File | Why it is here |
| --- | --- |
| `schema/*.json` | All six of the library's schemas, which refer to each other by `$id`. |
| `device-types/Huawei/NE8000-X8.yaml` | `part_number: 02351930`, which PyYAML reads as text, since it isn't octal, and YAML 1.2 as a number; 18 module bays. |
| `device-types/Eaton/Tripp-Lite-PDUMVR30HVNETLX.yaml` | Outlets named `08` and `09`, text to PyYAML; console, power and interface templates. |
| `device-types/Generic/wall-box-1-utp-plug.yaml` | `u_height: 0.0`, `is_powered: false`, and front and rear ports with `port-mappings`. |
| `device-types/IBM/BladeCenter-HS22.yaml` | A child device type, with only the fields the schema requires and `subdevice_role`. |
| `device-types/Generic/shelf-2he.yaml` | A parent device type, with device bays. |
| `device-types/Vertiv/SA1-01002.yaml` | `description`. |
| `device-types/3Com/4200G-12-Port.yml` | A file named `.yml`. |
| `device-types/Netgear/GS105.yaml` | Manufacturer `Netgear`, with `front_image` and `rear_image`. |
| `device-types/Netgear/GS510TLP.yml` | Manufacturer `NETGEAR`, in the same directory, which gets the same slug. |
| `device-types/GL-iNet/GL-AR150.yaml` | Manufacturer `GL-iNet`. |
| `device-types/GL-iNet/GL-MT300N-V2.yaml` | Manufacturer `GL.iNet`, which gets the same slug as `GL-iNet`. |
| `device-types/Unipi Technology/Axon-M205.yaml` | Manufacturer `Unipi Technology`, in a directory whose name has a space. |
| `device-types/Unipi Technology/Neuron-S103-RPi4.yaml` | Manufacturer `Unipi technology`, which differs only in case. |
| `device-types/Rohde & Schwarz/SITLine-ETH-L-10G.yaml` | `&` in a name, which NetBox's slug drops, though the library's own slugs spell it `and`. |
| `device-types/Testing/Test-1000.yaml` | Manufacturer `Generic` in another directory, and every kind of component the library has. |
| `device-types/Avocent/DSRIQ-VSN.yaml` | `_is_power_source`, and console server ports. |
| `device-types/APC/AP7516.yaml` | Power outlets with feed legs and power ports. |
| `device-types/Ubiquiti/UVC-G4-INS.yaml` | A wireless interface, with `rf_role`. |
| `device-types/HPE/ClearPass-C2000.yaml` | Both images, a weight in pounds and `airflow`. |
| `device-types/Apple/A1481.yaml` | A comment over several lines, and a weight in kilograms. |
