# The Moment and NFC Usage

A frictionless way to use NGC tag to communinicate with The Moment.

The moment currently has main tabs: Dashboard, History, Spools, Filaments, Printers, Settings.

This adds a new main tab NFCs. THe new order:

Dashboard, History, Spools, Filaments, Printers, NFCs, Settings.

## Goal

Improvemet NFC management for Spools, Locations and Filaments.

1. Use an NFC to idnetify an OpenPrinttag Filament spec
2. Use an NFC to identify a spool
3. Use and NFC to identify a location for a spool: toolead, inventory location, archive, trash
4. Spool of filament arrives, tap NFC Filament to identify it, add it into Spoolman, and optionally assign a NFC to the Spool
5. Move a spool to a location. Tap one of a Spool nfc or location nfc, and then tap the oposite, to assign them together.
6. Spool is spent, it is moved to archive, NFC spool becomes available for reuse.
7. A library of Filament NFC's to identify Filaments, and a way to write the NFC once, or redo in the case of failure and need to replace.
8. A library of Spool NFC's to be assigned and reused to spools as they come and go, or redo in the case of failure and need to replace.
9. A library of location NFC's typical done once, or redo in the case of failure and need to replace.

## Setup

- The Moment is the source of truth for all NFC IDs
- Each NFC has a URL to bring up The Moment in proper context

1. Create a new Filament NFC, and write the data to an NFC.
   1. In best case this is a complete OpenPrintTag record for the filament; OpenPrintTag record needs at least manufacturer, make, filament type and colour
   2. The  filament data is stored in Spoolman, the source of truth
   3. The Moment has CRUD to manage the Filament NFC ids
2. Create a new Spool NFC, and write the data to an NFC.
   1. The  filament data is stored in Spoolman, the source of truth
   2. The Moment has CRUD to manage the Spool NFC ids
3. Create a new Location NFC, and write the data to an NFC.
   1. The  Location data is stored in Spoolman, the source of truth
   2. The Moment has CRUD to manage the Location NFC ids

## Actions

1. A new spool arrives, use scans a Filament NFC that offers a URL to bring up a The Moment diaglog. Dialog has options:
   1. brings up the Filament details in Spoolman
   2. creates a new spool of the filament in Spoolman
      1. Offers linkings an available NFC Spool to this new spool
2. User taps a location nfc, then a spool nfc is tapped to say the spool is at this location
3. User taps a spool nfc, then a location nfc is tapped, to say the spool is at this location
4. User taps a spool nfc then taps a filament nfc, to say the filament for the ID is changed to this - confirmation can be needed
5. User taps a filament NFC then taps a spool NFC, , to say the filament for the ID is changed to this - confirmation can be needed
6. User needs to see if there are any available filament, location or spool nfc. Goal is to use one to assign, or to know if more are needed as none are available
7. User needs to manage the filament NFCs - which ids, which filament, any changes
8. User needs to manage the loaction NFCs - which ids, which locations, which spools and changes
9. User needs to manage the spool NFCs
10. Spool NFC can only have 0 or 1 Location, and 0 or 1, filament
11. Locations NFC can have 0.. many spools
12. Filament NFC can have 0.. many spools

## UI / UX

There is a new main tab for NFC Management

The NFC Management tab has sub tabs for each of spool, location, filament

There is existing NFC Spool Tags, NFC Location Tags, NFC Filament used to exist and may be commented out. It can be these three just needed to be collocated to the apprioriate sub tag and extended.

- Each sub tab:
  - has a table of the NFCs the moment is managing
  - There are Add and Delete buttons
  - Search field
- each NFC record has:
  - check box to delect
  - an unique ID, 
  - an optional human readable editable id, must be unique
  - if in use, its current assignment liked into its spoolman
  - edit button 
  - delete button
  

